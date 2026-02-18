package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disintegration/imaging"
	"github.com/rwcarlsen/goexif/exif"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

// ============================================================================
// Constants
// ============================================================================

const (
	MaxImageDimension   = 50000
	MaxPixelCount       = 500_000_000
	MaxDecompressedSize = 500 * 1024 * 1024
	MaxLogLines         = 10000
	PandocTimeout       = 120 * time.Minute
	StreamBufferSize    = 64 * 1024
	TargetDPI           = 96
	JPEGQuality         = 95
	MaxImageLongSide    = 2500
)

// ============================================================================
// Precomputed data (initialized once at process start)
// ============================================================================

// CRC32 lookup table for PNG chunk checksums.
var crc32PNGTable [256]uint32

// Precompiled regexps used by fixLaTeX / cleanMarkdown.
var (
	reBadTable = regexp.MustCompile(`\\begin\{longtable\}\[?\]?\{([^}]*)\}`)
	reCleanCol = regexp.MustCompile(`[^lrcpmbsd{}@>\\. \d]`)
	reImg      = regexp.MustCompile(`\\includegraphics(\[.*?\])?\{([^}]+)\}`)
	reBlankMD  = regexp.MustCompile(`\n{3,}`)
	reDivMD    = regexp.MustCompile(`</?div[^>]*>`)
	reSpanMD   = regexp.MustCompile(`</?span[^>]*>`)
)

func init() {
	for i := 0; i < 256; i++ {
		c := uint32(i)
		for j := 0; j < 8; j++ {
			if c&1 != 0 {
				c = 0xEDB88320 ^ (c >> 1)
			} else {
				c >>= 1
			}
		}
		crc32PNGTable[i] = c
	}
}

// ============================================================================
// Core types
// ============================================================================

// App is the main backend struct exposed to the Wails frontend.
type App struct {
	ctx       context.Context
	ctxCancel context.CancelFunc // cancel to tear down child processes on shutdown

	mu        sync.RWMutex
	logBuffer []string
	logSeq    int // monotonically increasing log sequence number

	currentJobID atomic.Value
	isProcessing atomic.Bool
}

// ConversionProgress is emitted to the frontend via Wails events.
type ConversionProgress struct {
	JobID        string  `json:"jobId"`
	Stage        string  `json:"stage"`
	Progress     float64 `json:"progress"`
	Message      string  `json:"message"`
	IsComplete   bool    `json:"isComplete"`
	IsError      bool    `json:"isError"`
	OutputPath   string  `json:"outputPath,omitempty"`
	MarkdownPath string  `json:"markdownPath,omitempty"`
	PDFPath      string  `json:"pdfPath,omitempty"`
}

// SanitizationReport describes what happened to a single image file.
type SanitizationReport struct {
	FilePath       string   `json:"filePath"`
	OriginalFormat string   `json:"originalFormat"`
	Actions        []string `json:"actions"`
	Status         string   `json:"status"`
	Error          string   `json:"error,omitempty"`
	FileSizeBefore int64    `json:"fileSizeBefore"`
	FileSizeAfter  int64    `json:"fileSizeAfter"`
}

// FontConfig holds platform-specific font names for LaTeX templates.
type FontConfig struct {
	MainFont    string
	CJKMainFont string
	CJKFallback string
	MonoFont    string
}

// ============================================================================
// Lifecycle
// ============================================================================

func NewApp() *App {
	return &App{
		logBuffer: make([]string, 0, 2000),
	}
}

// startup is called by Wails after the window is created.
func (a *App) startup(ctx context.Context) {
	// Derive a cancellable context so we can kill child processes on shutdown.
	derivedCtx, cancel := context.WithCancel(ctx)
	a.ctx = derivedCtx
	a.ctxCancel = cancel

	a.log("🔥 ATHANOR V4.3 — Optimized Edition")
	a.log(fmt.Sprintf("⚙️  Platform: %s/%s | CPUs: %d", runtime.GOOS, runtime.GOARCH, runtime.NumCPU()))
	a.log("🛡️  Protocols: MonsterKiller | DPI-Injector | ①②③-Fix | AI-Markdown")
	a.log("════════════════════════════════════════════════════════════════")
}

// Shutdown is called by Wails when the window is closed (register in main.go
// via OnShutdown). It cancels any running child-process contexts.
func (a *App) Shutdown(ctx context.Context) {
	a.log("🛑 应用关闭，清理子进程...")
	if a.ctxCancel != nil {
		a.ctxCancel()
	}
}

// ============================================================================
// Logging — incremental event-based approach
// ============================================================================

// log appends a timestamped message to the ring buffer and emits it to the
// frontend as an incremental event (with a sequence number so the frontend
// can detect gaps and request a backfill via GetLogsSince).
func (a *App) log(msg string) {
	a.mu.Lock()
	ts := time.Now().Format("15:04:05.000")
	line := fmt.Sprintf("[%s] %s", ts, msg)

	if len(a.logBuffer) >= MaxLogLines {
		a.logBuffer = a.logBuffer[MaxLogLines/5:]
	}
	a.logBuffer = append(a.logBuffer, line)
	seq := a.logSeq
	a.logSeq++
	a.mu.Unlock()

	fmt.Println(line)

	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "log:line", map[string]interface{}{
			"seq":  seq,
			"line": line,
		})
	}
}

// GetLogsSince returns all log lines whose sequence number >= `since`.
// The frontend calls this once on mount (since=0) and again if it detects
// a gap in the seq numbers it receives via events.
func (a *App) GetLogsSince(since int) map[string]interface{} {
	a.mu.RLock()
	defer a.mu.RUnlock()

	total := a.logSeq // next seq that will be assigned
	bufLen := len(a.logBuffer)

	// The ring buffer may have been trimmed, so the earliest available seq is:
	earliest := total - bufLen
	if earliest < 0 {
		earliest = 0
	}

	startIdx := since - earliest
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx >= bufLen {
		return map[string]interface{}{
			"lines":   []string{},
			"nextSeq": total,
		}
	}

	out := make([]string, bufLen-startIdx)
	copy(out, a.logBuffer[startIdx:])
	return map[string]interface{}{
		"lines":   out,
		"nextSeq": total,
	}
}

// ============================================================================
// File selection dialog
// ============================================================================

func (a *App) SelectEpub() (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("context not ready")
	}

	path, err := wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "选择 EPUB 文件",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "EPUB (*.epub)", Pattern: "*.epub;*.EPUB"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		a.log("⚠️  用户取消选择")
		return "", nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("无法访问: %w", err)
	}
	if info.IsDir() || info.Size() == 0 {
		return "", fmt.Errorf("无效文件")
	}

	a.log(fmt.Sprintf("🎯 目标: %s (%.2f MB)", filepath.Base(path), float64(info.Size())/1024/1024))
	return path, nil
}

// ============================================================================
// Main orchestrator
// ============================================================================

func (a *App) ConvertBook(inputPath string, outputFormat string) ConversionProgress {
	if !a.isProcessing.CompareAndSwap(false, true) {
		return a.fail("", "系统忙，请等待当前任务完成")
	}
	defer a.isProcessing.Store(false)

	jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())
	a.currentJobID.Store(jobID)
	result := ConversionProgress{JobID: jobID}

	fmtLower := strings.ToLower(outputFormat)
	wantPDF := strings.Contains(fmtLower, "pdf") || strings.Contains(fmtLower, "both") || strings.Contains(fmtLower, "all")
	wantMD := strings.Contains(fmtLower, "md") || strings.Contains(fmtLower, "markdown") || strings.Contains(fmtLower, "both") || strings.Contains(fmtLower, "all")
	if !wantPDF && !wantMD {
		wantPDF = true
	}

	a.progress(jobID, "init", 0, "🚀 初始化转换管道...")
	a.log(fmt.Sprintf("📤 输出模式: PDF=%v, Markdown=%v", wantPDF, wantMD))

	// Validate input.
	inputInfo, err := os.Stat(inputPath)
	if err != nil {
		return a.fail(jobID, fmt.Sprintf("文件不可访问: %v", err))
	}
	if !strings.HasSuffix(strings.ToLower(inputPath), ".epub") {
		return a.fail(jobID, "仅支持 EPUB 文件")
	}
	a.log(fmt.Sprintf("📖 输入: %s (%.2f MB)", filepath.Base(inputPath), float64(inputInfo.Size())/1024/1024))

	// Create isolated workspace.
	a.progress(jobID, "workspace", 5, "🏗️  创建隔离环境...")
	workDir, err := os.MkdirTemp("", "athanor_v4_*")
	if err != nil {
		return a.fail(jobID, fmt.Sprintf("工作空间失败: %v", err))
	}
	defer func() {
		a.log("🧹 清理工作空间...")
		if rmErr := os.RemoveAll(workDir); rmErr != nil {
			a.log(fmt.Sprintf("⚠️  清理失败: %v", rmErr))
		}
	}()

	// PDF pipeline.
	if wantPDF {
		a.progress(jobID, "pdf", 10, "📄 PDF 转换流水线启动...")
		pdfPath := outputPath(inputPath, "pdf")
		if err := a.toPDFOptimized(inputPath, pdfPath, workDir, jobID); err != nil {
			return a.fail(jobID, fmt.Sprintf("PDF 失败: %v\n💡 确保已安装 Pandoc + XeLaTeX", err))
		}

		pdfInfo, err := os.Stat(pdfPath)
		if err != nil {
			return a.fail(jobID, "PDF 文件未生成")
		}
		if pdfInfo.Size() < 1024 {
			return a.fail(jobID, fmt.Sprintf("PDF 异常小 (%d bytes)", pdfInfo.Size()))
		}

		result.PDFPath = pdfPath
		a.log(fmt.Sprintf("✅ PDF: %s (%.2f MB)", filepath.Base(pdfPath), float64(pdfInfo.Size())/1024/1024))
	}

	// Markdown pipeline.
	if wantMD {
		a.progress(jobID, "markdown", 90, "📝 生成 AI-Optimized Markdown...")
		mdPath := outputPath(inputPath, "md")
		if err := a.toMarkdown(inputPath, mdPath); err != nil {
			a.log(fmt.Sprintf("⚠️  Markdown 失败 (非致命): %v", err))
		} else {
			result.MarkdownPath = mdPath
			a.log(fmt.Sprintf("✅ Markdown: %s", mdPath))
		}
	}

	// Final result.
	if result.PDFPath != "" {
		result.OutputPath = result.PDFPath
	} else if result.MarkdownPath != "" {
		result.OutputPath = result.MarkdownPath
	}

	result.Stage = "complete"
	result.Progress = 100
	result.IsComplete = true
	result.Message = "转换成功"
	a.progress(jobID, "complete", 100, "✨ 全部完成！")
	return result
}

// ============================================================================
// Image sanitization engine (parallel, with fast path for clean JPEGs)
// ============================================================================

func (a *App) sanitizeAllImages(dir string) ([]SanitizationReport, error) {
	// Collect image paths.
	var imagePaths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			a.log(fmt.Sprintf("⚠️  遍历目录出错: %v", walkErr))
			return nil // continue walking
		}
		if !d.IsDir() && isImageExt(filepath.Ext(path)) {
			imagePaths = append(imagePaths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	total := len(imagePaths)
	if total == 0 {
		a.log("✨ 未发现图像文件")
		return nil, nil
	}

	// Parallel processing with bounded worker pool.
	workers := runtime.NumCPU()
	if workers > total {
		workers = total
	}
	if workers > 8 {
		workers = 8
	}
	a.log(fmt.Sprintf("🧼 发现 %d 个图像, %d 并行线程处理...", total, workers))

	reports := make([]SanitizationReport, total)
	var processed atomic.Int64

	var wg sync.WaitGroup
	jobs := make(chan int, workers*2)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				reports[idx] = a.sanitizeOne(imagePaths[idx])
				n := processed.Add(1)
				if n%50 == 0 || n == int64(total) {
					a.log(fmt.Sprintf("🧼 进度: %d/%d (%.0f%%)", n, total,
						float64(n)/float64(total)*100))
				}
			}
		}()
	}

	for i := range imagePaths {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	a.log(fmt.Sprintf("✨ 图像处理完成: %d 个", total))
	return reports, nil
}

func (a *App) sanitizeOne(path string) SanitizationReport {
	r := SanitizationReport{FilePath: path, Actions: make([]string, 0, 8), Status: "OK"}

	if info, err := os.Stat(path); err == nil {
		r.FileSizeBefore = info.Size()
	}

	// Fast path: clean JPEG that only needs DPI injection.
	if fr, ok := a.tryFastPath(path); ok {
		return *fr
	}

	// Full path: decode → fix → re-encode.
	realFmt, err := sniffFormat(path)
	if err != nil {
		r.Status = "FAILED"
		r.Error = err.Error()
		a.placeholder(path)
		r.Actions = append(r.Actions, "INVALID_REPLACED")
		return r
	}
	r.OriginalFormat = realFmt

	extFmt := extToFormat(filepath.Ext(path))
	if extFmt != "" && extFmt != realFmt {
		r.Actions = append(r.Actions, fmt.Sprintf("SPOOF_%s→%s", extFmt, realFmt))
	}

	img, err := decodeSafe(path, realFmt)
	if err != nil {
		r.Status = "REPLACED"
		r.Error = err.Error()
		a.placeholder(path)
		r.Actions = append(r.Actions, "DECODE_FAIL_REPLACED")
		return r
	}

	if rotated, act := exifRotate(path, img); act != "" {
		img = rotated
		r.Actions = append(r.Actions, act)
	}

	if normalized, act := toRGB(img); act != "" {
		img = normalized
		r.Actions = append(r.Actions, act)
	}

	if flat, act := flattenAlpha(img); act != "" {
		img = flat
		r.Actions = append(r.Actions, act)
	}

	if resized, act := resizeIfNeeded(img); act != "" {
		img = resized
		r.Actions = append(r.Actions, act)
	}

	ext := strings.ToLower(filepath.Ext(path))
	if err := reencode(path, img, ext); err != nil {
		r.Status = "FAILED"
		r.Error = err.Error()
		a.placeholder(path)
		r.Actions = append(r.Actions, "REENCODE_FAILED")
		return r
	}
	r.Actions = append(r.Actions, fmt.Sprintf("FORCE_%dDPI", TargetDPI), "CLEAN_BINARY")

	if info, err := os.Stat(path); err == nil {
		r.FileSizeAfter = info.Size()
	}

	if len(r.Actions) > 2 {
		r.Status = "REPAIRED"
	}
	return r
}

// tryFastPath handles clean JPEGs: no decode / re-encode, just DPI injection.
func (a *App) tryFastPath(path string) (*SanitizationReport, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".jpg" && ext != ".jpeg" {
		return nil, false
	}

	format, err := sniffFormat(path)
	if err != nil || format != "jpeg" {
		return nil, false
	}

	if needsExifRotation(path) {
		return nil, false
	}

	data, err := os.ReadFile(path)
	if err != nil || len(data) < 20 {
		return nil, false
	}

	beforeSize := int64(len(data))
	newData := injectJFIFDPI(data, TargetDPI)

	tmpPath := path + ".athanor_tmp"
	if err := os.WriteFile(tmpPath, newData, 0644); err != nil {
		return nil, false
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return nil, false
	}

	return &SanitizationReport{
		FilePath:       path,
		OriginalFormat: "jpeg",
		Actions:        []string{fmt.Sprintf("FAST_%dDPI", TargetDPI)},
		Status:         "OK",
		FileSizeBefore: beforeSize,
		FileSizeAfter:  int64(len(newData)),
	}, true
}

// needsExifRotation checks whether EXIF orientation requires pixel rotation.
func needsExifRotation(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		return false
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return false
	}
	orient, err := tag.Int(0)
	if err != nil {
		return false
	}
	return orient > 1
}

// resizeIfNeeded shrinks images whose longest side exceeds MaxImageLongSide.
func resizeIfNeeded(img image.Image) (image.Image, string) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	maxSide := w
	if h > maxSide {
		maxSide = h
	}
	if maxSide <= MaxImageLongSide {
		return img, ""
	}

	var newW, newH int
	if w > h {
		newW = MaxImageLongSide
		newH = int(float64(h) * float64(MaxImageLongSide) / float64(w))
	} else {
		newH = MaxImageLongSide
		newW = int(float64(w) * float64(MaxImageLongSide) / float64(h))
	}

	resized := imaging.Resize(img, newW, newH, imaging.Lanczos)
	return resized, fmt.Sprintf("RESIZE_%dx%d→%dx%d", w, h, newW, newH)
}

// ============================================================================
// Image format detection and decoding
// ============================================================================

func sniffFormat(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	head := make([]byte, 12)
	n, _ := io.ReadFull(f, head)
	if n < 2 {
		return "", fmt.Errorf("文件过小")
	}

	switch {
	case n >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return "jpeg", nil
	case n >= 8 && head[0] == 0x89 && string(head[1:4]) == "PNG":
		return "png", nil
	case n >= 6 && (string(head[:6]) == "GIF87a" || string(head[:6]) == "GIF89a"):
		return "gif", nil
	case n >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "webp", nil
	case n >= 2 && head[0] == 'B' && head[1] == 'M':
		return "bmp", nil
	case n >= 4 && (binary.LittleEndian.Uint32(head[:4]) == 0x002A4949 ||
		binary.BigEndian.Uint32(head[:4]) == 0x4D4D002A):
		return "tiff", nil
	default:
		return "", fmt.Errorf("未知格式 (magic: %X)", head[:minInt(4, n)])
	}
}

func extToFormat(ext string) string {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg":
		return "jpeg"
	case ".png":
		return "png"
	case ".gif":
		return "gif"
	case ".bmp":
		return "bmp"
	case ".tif", ".tiff":
		return "tiff"
	case ".webp":
		return "webp"
	}
	return ""
}

// decodeSafe reads image dimensions BEFORE allocating the full pixel buffer,
// defending against image bombs (e.g. a tiny PNG that decompresses to 10 GB).
func decodeSafe(path, format string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// --- Phase 1: read header only to get dimensions (no pixel alloc) ---
	var cfg image.Config
	var cfgErr error
	switch format {
	case "jpeg":
		cfg, cfgErr = jpeg.DecodeConfig(f)
	case "png":
		cfg, cfgErr = png.DecodeConfig(f)
	case "gif":
		cfg, cfgErr = gif.DecodeConfig(f)
	case "bmp":
		cfg, cfgErr = bmp.DecodeConfig(f)
	case "tiff":
		cfg, cfgErr = tiff.DecodeConfig(f)
	default:
		cfg, _, cfgErr = image.DecodeConfig(f)
	}
	if cfgErr != nil {
		return nil, fmt.Errorf("%s config: %w", format, cfgErr)
	}

	w, h := cfg.Width, cfg.Height
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("invalid dimensions: %dx%d", w, h)
	}
	if w > MaxImageDimension || h > MaxImageDimension {
		return nil, fmt.Errorf("monster image: %dx%d > %d", w, h, MaxImageDimension)
	}
	if int64(w)*int64(h) > MaxPixelCount {
		return nil, fmt.Errorf("pixel bomb: %dM pixels", int64(w)*int64(h)/1_000_000)
	}

	// --- Phase 2: seek back and decode the actual pixels ---
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek: %w", err)
	}

	lr := io.LimitReader(f, MaxDecompressedSize)

	var img image.Image
	var decErr error
	switch format {
	case "jpeg":
		img, decErr = jpeg.Decode(lr)
	case "png":
		img, decErr = png.Decode(lr)
	case "gif":
		img, decErr = gif.Decode(lr)
	case "bmp":
		img, decErr = bmp.Decode(lr)
	case "tiff":
		img, decErr = tiff.Decode(lr)
	default:
		img, _, decErr = image.Decode(lr)
	}
	if decErr != nil {
		return nil, fmt.Errorf("%s decode: %w", format, decErr)
	}

	return img, nil
}

// ============================================================================
// EXIF rotation and color-space normalization
// ============================================================================

func exifRotate(path string, img image.Image) (image.Image, string) {
	f, err := os.Open(path)
	if err != nil {
		return img, ""
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		return img, ""
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return img, ""
	}
	orient, err := tag.Int(0)
	if err != nil || orient <= 1 {
		return img, "EXIF_STRIPPED"
	}

	switch orient {
	case 2:
		return imaging.FlipH(img), "EXIF_FLIP_H"
	case 3:
		return imaging.Rotate180(img), "EXIF_ROT_180"
	case 4:
		return imaging.FlipV(img), "EXIF_FLIP_V"
	case 5:
		return imaging.Transpose(img), "EXIF_TRANSPOSE"
	case 6:
		return imaging.Rotate270(img), "EXIF_ROT_270"
	case 7:
		return imaging.Transverse(img), "EXIF_TRANSVERSE"
	case 8:
		return imaging.Rotate90(img), "EXIF_ROT_90"
	default:
		return img, "EXIF_STRIPPED"
	}
}

func toRGB(img image.Image) (image.Image, string) {
	switch img.(type) {
	case *image.RGBA, *image.NRGBA:
		return img, ""
	}
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)
	return rgba, "FORCE_sRGB"
}

// flattenAlpha composites semi-transparent images onto a white background.
// Uses direct pixel-slice access for performance.
func flattenAlpha(img image.Image) (image.Image, string) {
	bounds := img.Bounds()
	transparent := false

	switch v := img.(type) {
	case *image.NRGBA:
		pix := v.Pix
		stride := v.Stride
		step := 1
		if bounds.Dx()*bounds.Dy() > 1_000_000 {
			step = 10
		}
		for y := 0; y < bounds.Dy() && !transparent; y += step {
			rowOff := y * stride
			for x := 0; x < bounds.Dx(); x += step {
				if pix[rowOff+x*4+3] < 255 {
					transparent = true
					break
				}
			}
		}
	case *image.RGBA:
		pix := v.Pix
		stride := v.Stride
		step := 1
		if bounds.Dx()*bounds.Dy() > 1_000_000 {
			step = 10
		}
		for y := 0; y < bounds.Dy() && !transparent; y += step {
			rowOff := y * stride
			for x := 0; x < bounds.Dx(); x += step {
				if pix[rowOff+x*4+3] < 255 {
					transparent = true
					break
				}
			}
		}
	default:
		return img, ""
	}

	if !transparent {
		return img, ""
	}

	flat := image.NewRGBA(bounds)
	draw.Draw(flat, bounds, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, bounds, img, bounds.Min, draw.Over)
	return flat, "ALPHA_FLAT_WHITE"
}

// ============================================================================
// DPI-aware re-encoding
// ============================================================================

func reencode(path string, img image.Image, ext string) error {
	tmpPath := path + ".athanor_tmp"

	switch ext {
	case ".png":
		if err := savePNGWithDPI(tmpPath, img); err != nil {
			return err
		}
	default:
		if err := saveJPEGWithDPI(tmpPath, img); err != nil {
			return err
		}
	}

	return os.Rename(tmpPath, path)
}

func saveJPEGWithDPI(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return err
	}

	data := injectJFIFDPI(buf.Bytes(), TargetDPI)
	_, err = f.Write(data)
	return err
}

func injectJFIFDPI(data []byte, dpi int) []byte {
	if len(data) < 20 {
		return data
	}

	for i := 2; i < len(data)-16; i++ {
		if data[i] == 0xFF && data[i+1] == 0xE0 {
			if i+9 <= len(data) && string(data[i+4:i+9]) == "JFIF\x00" {
				data[i+11] = 0x01
				binary.BigEndian.PutUint16(data[i+12:i+14], uint16(dpi))
				binary.BigEndian.PutUint16(data[i+14:i+16], uint16(dpi))
				return data
			}
		}
	}

	jfif := []byte{
		0xFF, 0xE0,
		0x00, 0x10,
		'J', 'F', 'I', 'F', 0x00,
		0x01, 0x01,
		0x01,
		byte(dpi >> 8), byte(dpi),
		byte(dpi >> 8), byte(dpi),
		0x00, 0x00,
	}

	result := make([]byte, 0, len(data)+len(jfif))
	result = append(result, data[:2]...)
	result = append(result, jfif...)
	result = append(result, data[2:]...)
	return result
}

// savePNGWithDPI uses DefaultCompression (faster than BestCompression).
func savePNGWithDPI(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var buf bytes.Buffer
	enc := &png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return err
	}

	data := injectPNGpHYs(buf.Bytes(), TargetDPI)
	_, err = f.Write(data)
	return err
}

// injectPNGpHYs walks PNG chunks properly (instead of a raw bytes.Index)
// to avoid corrupting compressed IDAT data that happens to contain "pHYs".
func injectPNGpHYs(data []byte, dpi int) []byte {
	// PNG minimum: 8 (sig) + 25 (IHDR) = 33 bytes.
	if len(data) < 33 {
		return data
	}

	ppm := uint32(float64(dpi) / 0.0254)

	// Walk chunks starting after the 8-byte PNG signature.
	offset := 8
	ihdrEnd := -1
	for offset+12 <= len(data) {
		chunkLen := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		chunkType := string(data[offset+4 : offset+8])
		chunkTotal := 4 + 4 + chunkLen + 4 // length + type + data + crc

		if offset+chunkTotal > len(data) {
			break // truncated chunk, bail out
		}

		if chunkType == "IHDR" {
			ihdrEnd = offset + chunkTotal
		}

		if chunkType == "pHYs" && chunkLen == 9 {
			// Overwrite existing pHYs chunk data in-place.
			dataStart := offset + 8 // skip length + type
			binary.BigEndian.PutUint32(data[dataStart:dataStart+4], ppm)
			binary.BigEndian.PutUint32(data[dataStart+4:dataStart+8], ppm)
			data[dataStart+8] = 1 // unit = metre
			// Recompute CRC over type+data.
			crc := crc32PNG(data[offset+4 : offset+8+chunkLen])
			binary.BigEndian.PutUint32(data[offset+8+chunkLen:offset+8+chunkLen+4], crc)
			return data
		}

		offset += chunkTotal
	}

	// No existing pHYs — insert a new one right after IHDR.
	if ihdrEnd < 0 || ihdrEnd > len(data) {
		return data
	}

	var phys bytes.Buffer
	chunkData := make([]byte, 9)
	binary.BigEndian.PutUint32(chunkData[0:4], ppm)
	binary.BigEndian.PutUint32(chunkData[4:8], ppm)
	chunkData[8] = 1

	lengthBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBytes, 9)
	phys.Write(lengthBytes)

	typeAndData := append([]byte("pHYs"), chunkData...)
	phys.Write(typeAndData)

	crc := crc32PNG(typeAndData)
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc)
	phys.Write(crcBytes)

	result := make([]byte, 0, len(data)+phys.Len())
	result = append(result, data[:ihdrEnd]...)
	result = append(result, phys.Bytes()...)
	result = append(result, data[ihdrEnd:]...)
	return result
}

// crc32PNG computes CRC-32 using the precomputed lookup table.
func crc32PNG(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc = crc32PNGTable[byte(crc)^b] ^ (crc >> 8)
	}
	return crc ^ 0xFFFFFFFF
}

// placeholder replaces a corrupt image with a harmless SVG.
func (a *App) placeholder(path string) {
	svg := `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="400" height="300">
  <rect width="400" height="300" fill="#f8f8f8"/>
  <rect x="10" y="10" width="380" height="280" fill="none" stroke="#ddd" stroke-width="2" stroke-dasharray="8,4"/>
  <text x="200" y="140" text-anchor="middle" font-family="sans-serif" font-size="16" fill="#999">⚠️ 损坏图像已移除</text>
  <text x="200" y="165" text-anchor="middle" font-family="sans-serif" font-size="11" fill="#bbb">Corrupted Image Removed</text>
</svg>`
	svgPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".svg"
	if err := os.WriteFile(svgPath, []byte(svg), 0644); err != nil {
		a.log(fmt.Sprintf("⚠️  写入占位图失败: %v", err))
	}
	if err := os.Remove(path); err != nil {
		a.log(fmt.Sprintf("⚠️  移除损坏图片失败: %v", err))
	}
}

// ============================================================================
// EPUB container operations (zip / unzip)
// ============================================================================

func (a *App) unzipStreaming(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, zf := range r.File {
		fpath := filepath.Join(dest, zf.Name)

		// Zip-slip protection.
		if !strings.HasPrefix(filepath.Clean(fpath), filepath.Clean(dest)+string(os.PathSeparator)) {
			a.log(fmt.Sprintf("⚠️  跳过危险路径: %s", zf.Name))
			continue
		}

		if zf.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		os.MkdirAll(filepath.Dir(fpath), os.ModePerm)

		if err := extractFile(zf, fpath); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(zf *zip.File, dest string) error {
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, zf.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, StreamBufferSize)
	_, err = io.CopyBuffer(out, rc, buf)
	return err
}

func (a *App) zipEPUBStrict(srcDir, destFile string) error {
	f, err := os.Create(destFile)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	mtPath := filepath.Join(srcDir, "mimetype")
	if mtData, err := os.ReadFile(mtPath); err == nil {
		header := &zip.FileHeader{
			Name:   "mimetype",
			Method: zip.Store,
		}
		writer, err := w.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := writer.Write(bytes.TrimSpace(mtData)); err != nil {
			return fmt.Errorf("write mimetype: %w", err)
		}
	}

	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || filepath.Base(path) == "mimetype" {
			return walkErr
		}

		relPath, _ := filepath.Rel(srcDir, path)
		relPath = filepath.ToSlash(relPath)

		info, err := d.Info()
		if err != nil {
			return err
		}

		header, _ := zip.FileInfoHeader(info)
		header.Name = relPath
		header.Method = zip.Deflate

		writer, err := w.CreateHeader(header)
		if err != nil {
			return err
		}

		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()

		buf := make([]byte, StreamBufferSize)
		_, err = io.CopyBuffer(writer, src, buf)
		return err
	})
}

// ============================================================================
// PDF generation pipeline
// ============================================================================

func getFontConfig() FontConfig {
	switch runtime.GOOS {
	case "windows":
		return FontConfig{
			MainFont:    "Times New Roman",
			CJKMainFont: "Microsoft YaHei",
			CJKFallback: "Segoe UI Symbol",
			MonoFont:    "Consolas",
		}
	case "darwin":
		return FontConfig{
			MainFont:    "Times New Roman",
			CJKMainFont: "PingFang SC",
			CJKFallback: "Apple Symbols",
			MonoFont:    "Menlo",
		}
	default:
		return FontConfig{
			MainFont:    "DejaVu Serif",
			CJKMainFont: "Noto Sans CJK SC",
			CJKFallback: "Noto Sans Symbols2",
			MonoFont:    "DejaVu Sans Mono",
		}
	}
}

// analyzeEpub scans the zip directory (without extracting) to estimate
// content complexity so we can choose the right LaTeX engine.
func analyzeEpub(epubPath string) (sizeMB float64, imageCount int, totalTextFiles int) {
	info, err := os.Stat(epubPath)
	if err != nil {
		return 0, 0, 0
	}
	sizeMB = float64(info.Size()) / 1024 / 1024

	r, err := zip.OpenReader(epubPath)
	if err != nil {
		return sizeMB, 0, 0
	}
	defer r.Close()

	for _, f := range r.File {
		ext := strings.ToLower(filepath.Ext(f.Name))
		if isImageExt(ext) {
			imageCount++
		}
		if ext == ".xhtml" || ext == ".html" || ext == ".htm" || ext == ".xml" {
			totalTextFiles++
		}
	}
	return
}

// toPDFOptimized runs: Pandoc (gen tex + extract media) → sanitize → fix → compile.
func (a *App) toPDFOptimized(inputEpub, outputPdf, workDir, jobID string) error {
	if _, err := exec.LookPath("pandoc"); err != nil {
		return fmt.Errorf("Pandoc 未安装")
	}

	a.ensureLaTeXPackages()

	fc := getFontConfig()
	a.log(fmt.Sprintf("🔤 字体: Main=%s CJK=%s Fallback=%s Mono=%s",
		fc.MainFont, fc.CJKMainFont, fc.CJKFallback, fc.MonoFont))

	// Choose LaTeX engine based on content complexity.
	epubSizeMB, imageCount, textFiles := analyzeEpub(inputEpub)
	a.log(fmt.Sprintf("📊 EPUB 分析: %.1fMB, %d 张图片, %d 个文本文件",
		epubSizeMB, imageCount, textFiles))

	useLua := epubSizeMB > 15 || imageCount > 80 || textFiles > 50
	engine := "xelatex"
	if useLua {
		engine = "lualatex"
	}

	// Fall back if the chosen engine is not installed.
	if _, err := exec.LookPath(engine); err != nil {
		fallback := "xelatex"
		if engine == "xelatex" {
			fallback = "lualatex"
		}
		if _, err2 := exec.LookPath(fallback); err2 != nil {
			return fmt.Errorf("未找到 %s 或 %s", engine, fallback)
		}
		a.log(fmt.Sprintf("⚠️  %s 不可用，回退到 %s", engine, fallback))
		engine = fallback
		useLua = (engine == "lualatex")
	}

	a.log(fmt.Sprintf("⚙️  引擎: %s (size=%.1fMB imgs=%d texts=%d)",
		engine, epubSizeMB, imageCount, textFiles))

	a.prewarmFontCache(engine)

	templatePath := filepath.Join(workDir, "athanor_template.tex")
	var templateContent string
	if useLua {
		templateContent = buildLuaLaTeXTemplate(fc)
	} else {
		templateContent = buildXeLaTeXTemplate(fc)
	}
	if err := os.WriteFile(templatePath, []byte(templateContent), 0644); err != nil {
		return fmt.Errorf("模板写入失败: %w", err)
	}

	// Step 1: Pandoc generates .tex and extracts media.
	texPath := filepath.Join(workDir, "output.tex")
	mediaDir := workDir

	a.log("📝 第1步: Pandoc 生成 LaTeX 源码 + 提取媒体...")
	a.progress(jobID, "pdf", 12, "📝 Pandoc 解析 EPUB...")

	pandocArgs := []string{
		inputEpub,
		"-o", texPath,
		"--template=" + templatePath,
		"--extract-media=" + mediaDir,
		"--toc",
		"--toc-depth=2",
		"-V", "geometry:margin=1in",
		"-V", fmt.Sprintf("mainfont=%s", fc.MainFont),
		"-V", fmt.Sprintf("monofont=%s", fc.MonoFont),
		"-V", fmt.Sprintf("CJKmainfont=%s", fc.CJKMainFont),
		"-M", "date=",
	}

	if err := a.runPandoc(pandocArgs, jobID); err != nil {
		return fmt.Errorf("Pandoc 生成 tex 失败: %w", err)
	}

	texInfo, err := os.Stat(texPath)
	if err != nil || texInfo.Size() < 100 {
		return fmt.Errorf("LaTeX 源码未生成或过小")
	}
	a.log(fmt.Sprintf("✅ LaTeX 源码: %.2f MB", float64(texInfo.Size())/1024/1024))

	// Step 2: Sanitize extracted media.
	a.progress(jobID, "sanitize", 30, "🧼 并行图像净化...")
	extractedMediaDir := filepath.Join(workDir, "media")
	if _, err := os.Stat(extractedMediaDir); err == nil {
		reports, sErr := a.sanitizeAllImages(extractedMediaDir)
		if sErr != nil {
			a.log(fmt.Sprintf("⚠️  净化出错 (继续): %v", sErr))
		} else {
			a.printSanitizeStats(reports)
		}
	} else {
		reports, sErr := a.sanitizeAllImages(workDir)
		if sErr != nil {
			a.log(fmt.Sprintf("⚠️  净化出错 (继续): %v", sErr))
		} else if len(reports) > 0 {
			a.printSanitizeStats(reports)
		}
	}

	// Step 3: Fix LaTeX source.
	a.progress(jobID, "pdf", 55, "🔧 修复 LaTeX 源码...")
	if err := a.fixLaTeX(texPath, workDir); err != nil {
		a.log(fmt.Sprintf("⚠️  LaTeX 修复出错 (继续): %v", err))
	}

	// Step 4: Compile.
	a.log(fmt.Sprintf("📄 第4步: %s 编译 PDF...", engine))
	a.progress(jobID, "pdf", 60, fmt.Sprintf("📄 %s 编译中...", engine))

	if err := a.runLaTeX(engine, texPath, workDir, jobID); err != nil {
		return fmt.Errorf("LaTeX 编译失败: %w", err)
	}

	// Step 5: Copy PDF to output location.
	compiledPdf := filepath.Join(workDir, "output.pdf")
	pdfInfo, err := os.Stat(compiledPdf)
	if err != nil {
		return fmt.Errorf("PDF 未生成: %w", err)
	}
	if pdfInfo.Size() < 1024 {
		return fmt.Errorf("PDF 异常小 (%d bytes)", pdfInfo.Size())
	}

	srcFile, err := os.Open(compiledPdf)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(outputPdf)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	a.log(fmt.Sprintf("✅ PDF 编译完成: %.2f MB", float64(pdfInfo.Size())/1024/1024))
	return nil
}

// fixLaTeX patches common issues in Pandoc-generated LaTeX.
// Builds an image-path index once (O(n)) instead of walking per missing image.
func (a *App) fixLaTeX(texPath, workDir string) error {
	data, err := os.ReadFile(texPath)
	if err != nil {
		return err
	}

	content := string(data)
	fixCount := 0

	// Fix 1: Clean up broken longtable column specs.
	content = reBadTable.ReplaceAllStringFunc(content, func(match string) string {
		sub := reBadTable.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		colSpec := sub[1]
		cleaned := reCleanCol.ReplaceAllString(colSpec, "")
		if cleaned == "" {
			cleaned = "@{}l@{}"
		}
		if cleaned != colSpec {
			fixCount++
			return fmt.Sprintf(`\begin{longtable}[]{%s}`, cleaned)
		}
		return match
	})

	content = strings.ReplaceAll(content, `\begin{longtable}[]{@{}@{}}`, `\begin{longtable}[]{@{}l@{}}`)

	// Build filename → relative-path index for image lookup.
	imageIndex := make(map[string]string)
	if walkErr := filepath.WalkDir(workDir, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			a.log(fmt.Sprintf("⚠️  遍历工作目录出错: %v", e))
			return nil
		}
		if !d.IsDir() && isImageExt(filepath.Ext(p)) {
			rel, _ := filepath.Rel(workDir, p)
			imageIndex[filepath.Base(p)] = filepath.ToSlash(rel)
		}
		return nil
	}); walkErr != nil {
		a.log(fmt.Sprintf("⚠️  构建图片索引失败: %v", walkErr))
	}

	// Fix 2: Repair broken image paths.
	content = reImg.ReplaceAllStringFunc(content, func(match string) string {
		sub := reImg.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		opts := sub[1]
		imgPath := sub[2]

		absPath := imgPath
		if !filepath.IsAbs(imgPath) {
			absPath = filepath.Join(workDir, imgPath)
		}

		if _, err := os.Stat(absPath); err == nil {
			return match
		}

		mediaPath := filepath.Join(workDir, "media", imgPath)
		if _, err := os.Stat(mediaPath); err == nil {
			fixCount++
			return fmt.Sprintf(`\includegraphics%s{media/%s}`, opts, imgPath)
		}

		baseName := filepath.Base(imgPath)
		if found, ok := imageIndex[baseName]; ok {
			fixCount++
			return fmt.Sprintf(`\includegraphics%s{%s}`, opts, found)
		}

		fixCount++
		return fmt.Sprintf(`%% MISSING: \includegraphics%s{%s}`, opts, imgPath)
	})

	if fixCount > 0 {
		a.log(fmt.Sprintf("🔧 修复了 %d 处 LaTeX 问题", fixCount))
	}

	return os.WriteFile(texPath, []byte(content), 0644)
}

// runLaTeX compiles the .tex file with the chosen engine (xelatex / lualatex).
// Uses a derived context from a.ctx so child processes are killed on app shutdown.
func (a *App) runLaTeX(engine, texPath, workDir, jobID string) error {
	pageRe := regexp.MustCompile(`\[(\d+)`)

	// Dynamic per-pass timeout based on .tex size.
	texInfo, err := os.Stat(texPath)
	if err != nil {
		return fmt.Errorf("cannot stat tex file: %w", err)
	}
	texSizeMB := float64(texInfo.Size()) / 1024 / 1024
	perPassTimeout := time.Duration(texSizeMB*3+5) * time.Minute
	if perPassTimeout > 90*time.Minute {
		perPassTimeout = 90 * time.Minute
	}
	if perPassTimeout < 8*time.Minute {
		perPassTimeout = 8 * time.Minute
	}
	if engine == "lualatex" {
		perPassTimeout = perPassTimeout * 2
	}

	stallTimeout := 3 * time.Minute
	if engine == "lualatex" {
		stallTimeout = 5 * time.Minute
	}

	a.log(fmt.Sprintf("⏱️  编译超时: %.0f分钟/遍, 卡死检测: %.0f分钟 (tex=%.1fMB)",
		perPassTimeout.Minutes(), stallTimeout.Minutes(), texSizeMB))

	for pass := 1; pass <= 2; pass++ {
		a.log(fmt.Sprintf("📄 %s 第 %d 遍...", engine, pass))

		isDraft := (pass == 1)
		if isDraft {
			a.log("⚡ 第1遍: draft 模式 (跳过图片处理，仅建立引用)")
		} else {
			a.log("📄 第2遍: 完整编译 (含图片)")
		}

		if jobID != "" {
			pct := 60.0 + float64(pass-1)*15.0
			label := ""
			if isDraft {
				label = " (快速)"
			}
			a.progress(jobID, "pdf", pct, fmt.Sprintf("📄 编译第 %d/2 遍%s...", pass, label))
		}

		// Derive timeout context from a.ctx (not context.Background) so that
		// app shutdown cancels child processes.
		ctx, cancel := context.WithTimeout(a.ctx, perPassTimeout)

		args := []string{
			"-interaction=nonstopmode",
			"-output-directory=" + workDir,
		}
		if isDraft {
			args = append(args, "-draftmode")
		}
		args = append(args, texPath)

		cmd := exec.CommandContext(ctx, engine, args...)
		cmd.Dir = workDir
		hideCmdWindow(cmd)

		stdoutPipe, pipeErr := cmd.StdoutPipe()
		if pipeErr != nil {
			cancel()
			return pipeErr
		}
		cmd.Stderr = cmd.Stdout

		if err := cmd.Start(); err != nil {
			cancel()
			return fmt.Errorf("%s 启动失败: %w", engine, err)
		}

		var lastActivity atomic.Value
		lastActivity.Store(time.Now())

		var outputBuf bytes.Buffer
		readDone := make(chan struct{})

		// Reader goroutine.
		go func() {
			defer close(readDone)
			buf := make([]byte, 4096)
			lastPage := 0
			lastLogTime := time.Now()

			for {
				n, readErr := stdoutPipe.Read(buf)
				if n > 0 {
					chunk := string(buf[:n])
					outputBuf.WriteString(chunk)
					lastActivity.Store(time.Now())

					matches := pageRe.FindAllStringSubmatch(chunk, -1)
					for _, m := range matches {
						if len(m) > 1 {
							page := 0
							fmt.Sscanf(m[1], "%d", &page)
							if page > lastPage+50 || time.Since(lastLogTime) > 8*time.Second {
								msg := fmt.Sprintf("📄 第%d遍 · 第 %d 页", pass, page)
								a.log(msg)
								if jobID != "" {
									pct := 60.0 + float64(pass-1)*15.0 + float64(page%500)/500.0*12.0
									if pct > 88 {
										pct = 88
									}
									a.progress(jobID, "pdf", pct, msg)
								}
								lastPage = page
								lastLogTime = time.Now()
							}
						}
					}
				}
				if readErr != nil {
					break
				}
			}
		}()

		// Watchdog goroutine — kills process if stalled.
		watchdogDone := make(chan struct{})
		go func() {
			defer close(watchdogDone)
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					last := lastActivity.Load().(time.Time)
					silent := time.Since(last)
					if silent > stallTimeout {
						a.log(fmt.Sprintf("⚠️  %s 第%d遍卡死 (%.0f分钟无输出)，强制终止",
							engine, pass, silent.Minutes()))
						cancel()
						return
					}
					if silent > 1*time.Minute {
						a.log(fmt.Sprintf("⏳ %s 第%d遍已沉默 %.0f秒...",
							engine, pass, silent.Seconds()))
					}
				case <-readDone:
					return
				case <-ctx.Done():
					return
				}
			}
		}()

		waitErr := cmd.Wait()
		<-readDone
		<-watchdogDone
		cancel()

		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%s 第%d遍超时/卡死", engine, pass)
		}

		// If app is shutting down, abort immediately.
		if a.ctx.Err() != nil {
			return fmt.Errorf("应用关闭，编译中止")
		}

		if isDraft {
			errCount := countErrors(outputBuf.String())
			if errCount > 0 {
				a.log(fmt.Sprintf("⚠️  第%d遍(draft): %d 个非致命错误", pass, errCount))
			}
			continue
		}

		// Second pass: check that a PDF was actually produced.
		pdfPath := filepath.Join(workDir, "output.pdf")
		if info, statErr := os.Stat(pdfPath); statErr == nil && info.Size() > 1024 {
			errCount := countErrors(outputBuf.String())
			if errCount > 0 {
				a.log(fmt.Sprintf("⚠️  第%d遍: %d 个非致命错误（已跳过）", pass, errCount))
			}
			continue
		}

		if waitErr != nil {
			errStr := outputBuf.String()
			if len(errStr) > 2000 {
				errStr = errStr[len(errStr)-2000:]
			}
			a.log(fmt.Sprintf("❌ %s 第%d遍输出:\n%s", engine, pass, errStr))
			return fmt.Errorf("%s 第%d遍失败", engine, pass)
		}
	}

	return nil
}

// ============================================================================
// LaTeX templates
// ============================================================================

func buildXeLaTeXTemplate(fc FontConfig) string {
	template := `\documentclass[12pt,a4paper]{article}

% ═══════ CORE PACKAGES ═══════
\usepackage{amsmath,amssymb}
\usepackage{fontspec}
\usepackage{xeCJK}
\usepackage{geometry}
\usepackage{graphicx}
\usepackage{hyperref}
\usepackage{longtable}
\usepackage{booktabs}
\usepackage{array}
\usepackage{xcolor}
\usepackage{etoolbox}

% ═══════ OPTIONAL PACKAGES ═══════
\IfFileExists{caption.sty}{\usepackage{caption}}{}
\IfFileExists{fvextra.sty}{\usepackage{fvextra}}{\usepackage{fancyvrb}}
\IfFileExists{framed.sty}{\usepackage{framed}}{}
\IfFileExists{upquote.sty}{\usepackage{upquote}}{}

% ═══════ PAGE LAYOUT ═══════
\geometry{a4paper, margin=1in}

% ═══════ FONTS ═══════
\setmainfont{<<MAINFONT>>}
\setmonofont{<<MONOFONT>>}[Scale=0.85]
\setCJKmainfont{<<CJKMAINFONT>>}

% ═══════ CIRCLED NUMBERS FIX ═══════
\xeCJKDeclareCharClass{CJK}{
  "2460 -> "24FF,
  "2600 -> "26FF,
  "2700 -> "27BF,
  "3200 -> "32FF
}
\setCJKfallbackfamilyfont{\CJKrmdefault}{<<CJKFALLBACK>>}

% ═══════ IMAGE CENTERING ═══════
\makeatletter
\g@addto@macro\@floatboxreset{\centering}
\makeatother
\providecommand{\pandocbounded}[1]{\begin{center}#1\end{center}}

% ═══════ PANDOC 3.x COMPATIBILITY ═══════
\providecommand{\tightlist}{%
  \setlength{\itemsep}{0pt}\setlength{\parskip}{0pt}}
\newlength{\cslhangindent}
\setlength{\cslhangindent}{1.5em}
\newlength{\csllabelwidth}
\setlength{\csllabelwidth}{3em}
\newenvironment{CSLReferences}[2]{}{}

% ═══════ COUNTER FIX ═══════
\makeatletter
\@ifundefined{c@none}{\newcounter{none}}{}
\AtBeginDocument{%
  \@ifundefined{c@none}{\newcounter{none}}{}%
  \@ifpackageloaded{caption}{\captionsetup[longtable]{labelformat=empty}}{}%
}
\makeatother

% ═══════ SHADED CODE BLOCKS ═══════
\definecolor{shadecolor}{RGB}{245,245,245}
\IfFileExists{framed.sty}{%
  \newenvironment{Shaded}{\begin{snugshade}}{\end{snugshade}}
}{%
  \newenvironment{Shaded}{\begin{quote}}{\end{quote}}
}
\DefineVerbatimEnvironment{Highlighting}{Verbatim}{
  commandchars=\\\{\},
  fontsize=\small,
  baselinestretch=1.1
}

% ═══════ SYNTAX TOKENS ═══════
\providecommand{\AlertTok}[1]{\textcolor[rgb]{1.00,0.00,0.00}{\textbf{#1}}}
\providecommand{\AnnotationTok}[1]{\textcolor[rgb]{0.38,0.63,0.69}{\textbf{\textit{#1}}}}
\providecommand{\AttributeTok}[1]{\textcolor[rgb]{0.49,0.56,0.16}{#1}}
\providecommand{\BaseNTok}[1]{\textcolor[rgb]{0.25,0.63,0.44}{#1}}
\providecommand{\BuiltInTok}[1]{#1}
\providecommand{\CharTok}[1]{\textcolor[rgb]{0.25,0.44,0.63}{#1}}
\providecommand{\CommentTok}[1]{\textcolor[rgb]{0.38,0.63,0.69}{\textit{#1}}}
\providecommand{\CommentVarTok}[1]{\textcolor[rgb]{0.38,0.63,0.69}{\textbf{\textit{#1}}}}
\providecommand{\ConstantTok}[1]{\textcolor[rgb]{0.53,0.00,0.00}{#1}}
\providecommand{\ControlFlowTok}[1]{\textcolor[rgb]{0.00,0.44,0.13}{\textbf{#1}}}
\providecommand{\DataTypeTok}[1]{\textcolor[rgb]{0.56,0.13,0.00}{#1}}
\providecommand{\DecValTok}[1]{\textcolor[rgb]{0.25,0.63,0.44}{#1}}
\providecommand{\DocumentationTok}[1]{\textcolor[rgb]{0.73,0.13,0.13}{\textit{#1}}}
\providecommand{\ErrorTok}[1]{\textcolor[rgb]{1.00,0.00,0.00}{\textbf{#1}}}
\providecommand{\ExtensionTok}[1]{#1}
\providecommand{\FloatTok}[1]{\textcolor[rgb]{0.25,0.63,0.44}{#1}}
\providecommand{\FunctionTok}[1]{\textcolor[rgb]{0.02,0.16,0.49}{#1}}
\providecommand{\ImportTok}[1]{#1}
\providecommand{\InformationTok}[1]{\textcolor[rgb]{0.38,0.63,0.69}{\textbf{\textit{#1}}}}
\providecommand{\KeywordTok}[1]{\textcolor[rgb]{0.00,0.44,0.13}{\textbf{#1}}}
\providecommand{\NormalTok}[1]{#1}
\providecommand{\OperatorTok}[1]{\textcolor[rgb]{0.40,0.40,0.40}{#1}}
\providecommand{\OtherTok}[1]{\textcolor[rgb]{0.00,0.44,0.13}{#1}}
\providecommand{\PreprocessorTok}[1]{\textcolor[rgb]{0.74,0.48,0.00}{#1}}
\providecommand{\RegionMarkerTok}[1]{#1}
\providecommand{\SpecialCharTok}[1]{\textcolor[rgb]{0.25,0.44,0.63}{#1}}
\providecommand{\SpecialStringTok}[1]{\textcolor[rgb]{0.25,0.44,0.63}{#1}}
\providecommand{\StringTok}[1]{\textcolor[rgb]{0.25,0.44,0.63}{#1}}
\providecommand{\VariableTok}[1]{\textcolor[rgb]{0.10,0.09,0.49}{#1}}
\providecommand{\VerbatimStringTok}[1]{\textcolor[rgb]{0.25,0.44,0.63}{#1}}
\providecommand{\WarningTok}[1]{\textcolor[rgb]{0.38,0.63,0.69}{\textbf{\textit{#1}}}}

% ═══════ IMAGE SCALING ═══════
\makeatletter
\def\maxwidth{\ifdim\Gin@nat@width>\linewidth\linewidth\else\Gin@nat@width\fi}
\def\maxheight{\ifdim\Gin@nat@height>0.8\textheight 0.8\textheight\else\Gin@nat@height\fi}
\makeatother
\setkeys{Gin}{width=\maxwidth,height=\maxheight,keepaspectratio}

\hypersetup{
  colorlinks=true,
  linkcolor=blue,
  urlcolor=blue,
  unicode=true,
  pdfencoding=auto,
  bookmarksnumbered=true
}

\setlength{\parskip}{6pt plus 2pt minus 1pt}
\setlength{\parindent}{0pt}
\setlength{\emergencystretch}{3em}

\begin{document}

$if(title)$
\title{$title$}
$endif$
$if(author)$
\author{$for(author)$$author$$sep$ \and $endfor$}
$endif$
\date{}
$if(title)$
\maketitle
$endif$

$if(toc)$
{
\hypersetup{linkcolor=black}
\setcounter{tocdepth}{$if(toc-depth)$$toc-depth$$else$3$endif$}
\tableofcontents
}
\clearpage
$endif$

$body$

\end{document}
`

	replacer := strings.NewReplacer(
		"<<MAINFONT>>", fc.MainFont,
		"<<MONOFONT>>", fc.MonoFont,
		"<<CJKMAINFONT>>", fc.CJKMainFont,
		"<<CJKFALLBACK>>", fc.CJKFallback,
	)
	return replacer.Replace(template)
}

func buildLuaLaTeXTemplate(fc FontConfig) string {
	template := `\documentclass[12pt,a4paper]{article}

% ═══════ CORE PACKAGES ═══════
\usepackage{amsmath,amssymb}
\usepackage{fontspec}
\usepackage{luatexja-fontspec}
\usepackage{geometry}
\usepackage{graphicx}
\usepackage{hyperref}
\usepackage{longtable}
\usepackage{booktabs}
\usepackage{array}
\usepackage{xcolor}
\usepackage{etoolbox}

% ═══════ OPTIONAL PACKAGES ═══════
\IfFileExists{caption.sty}{\usepackage{caption}}{}
\IfFileExists{fvextra.sty}{\usepackage{fvextra}}{\usepackage{fancyvrb}}
\IfFileExists{framed.sty}{\usepackage{framed}}{}
\IfFileExists{upquote.sty}{\usepackage{upquote}}{}

% ═══════ PAGE LAYOUT ═══════
\geometry{a4paper, margin=1in}

% ═══════ WESTERN FONTS ═══════
\setmainfont{<<MAINFONT>>}
\setmonofont{<<MONOFONT>>}[Scale=0.85]

% ═══════ CJK FONTS ═══════
\setmainjfont{<<CJKMAINFONT>>}
\setsansjfont{<<CJKMAINFONT>>}

% ═══════ SYMBOL FALLBACK ═══════
\ltjsetparameter{jacharrange={-2}}
\newjfontfamily\symboljfont{<<CJKFALLBACK>>}
\ltjsetparameter{alxspmode={"2460,allow}}
\ltjsetparameter{alxspmode={"2461,allow}}
\ltjsetparameter{alxspmode={"2462,allow}}
\ltjsetparameter{alxspmode={"2463,allow}}
\ltjsetparameter{alxspmode={"2464,allow}}

% ═══════ IMAGE CENTERING ═══════
\makeatletter
\g@addto@macro\@floatboxreset{\centering}
\makeatother
\providecommand{\pandocbounded}[1]{\begin{center}#1\end{center}}

% ═══════ PANDOC 3.x COMPATIBILITY ═══════
\providecommand{\tightlist}{%
  \setlength{\itemsep}{0pt}\setlength{\parskip}{0pt}}
\newlength{\cslhangindent}
\setlength{\cslhangindent}{1.5em}
\newlength{\csllabelwidth}
\setlength{\csllabelwidth}{3em}
\newenvironment{CSLReferences}[2]{}{}

% ═══════ COUNTER FIX ═══════
\makeatletter
\@ifundefined{c@none}{\newcounter{none}}{}
\AtBeginDocument{%
  \@ifundefined{c@none}{\newcounter{none}}{}%
  \@ifpackageloaded{caption}{\captionsetup[longtable]{labelformat=empty}}{}%
}
\makeatother

% ═══════ SHADED CODE BLOCKS ═══════
\definecolor{shadecolor}{RGB}{245,245,245}
\IfFileExists{framed.sty}{%
  \newenvironment{Shaded}{\begin{snugshade}}{\end{snugshade}}
}{%
  \newenvironment{Shaded}{\begin{quote}}{\end{quote}}
}
\DefineVerbatimEnvironment{Highlighting}{Verbatim}{
  commandchars=\\\{\},
  fontsize=\small,
  baselinestretch=1.1
}

% ═══════ SYNTAX TOKENS ═══════
\providecommand{\AlertTok}[1]{\textcolor[rgb]{1.00,0.00,0.00}{\textbf{#1}}}
\providecommand{\AnnotationTok}[1]{\textcolor[rgb]{0.38,0.63,0.69}{\textbf{\textit{#1}}}}
\providecommand{\AttributeTok}[1]{\textcolor[rgb]{0.49,0.56,0.16}{#1}}
\providecommand{\BaseNTok}[1]{\textcolor[rgb]{0.25,0.63,0.44}{#1}}
\providecommand{\BuiltInTok}[1]{#1}
\providecommand{\CharTok}[1]{\textcolor[rgb]{0.25,0.44,0.63}{#1}}
\providecommand{\CommentTok}[1]{\textcolor[rgb]{0.38,0.63,0.69}{\textit{#1}}}
\providecommand{\CommentVarTok}[1]{\textcolor[rgb]{0.38,0.63,0.69}{\textbf{\textit{#1}}}}
\providecommand{\ConstantTok}[1]{\textcolor[rgb]{0.53,0.00,0.00}{#1}}
\providecommand{\ControlFlowTok}[1]{\textcolor[rgb]{0.00,0.44,0.13}{\textbf{#1}}}
\providecommand{\DataTypeTok}[1]{\textcolor[rgb]{0.56,0.13,0.00}{#1}}
\providecommand{\DecValTok}[1]{\textcolor[rgb]{0.25,0.63,0.44}{#1}}
\providecommand{\DocumentationTok}[1]{\textcolor[rgb]{0.73,0.13,0.13}{\textit{#1}}}
\providecommand{\ErrorTok}[1]{\textcolor[rgb]{1.00,0.00,0.00}{\textbf{#1}}}
\providecommand{\ExtensionTok}[1]{#1}
\providecommand{\FloatTok}[1]{\textcolor[rgb]{0.25,0.63,0.44}{#1}}
\providecommand{\FunctionTok}[1]{\textcolor[rgb]{0.02,0.16,0.49}{#1}}
\providecommand{\ImportTok}[1]{#1}
\providecommand{\InformationTok}[1]{\textcolor[rgb]{0.38,0.63,0.69}{\textbf{\textit{#1}}}}
\providecommand{\KeywordTok}[1]{\textcolor[rgb]{0.00,0.44,0.13}{\textbf{#1}}}
\providecommand{\NormalTok}[1]{#1}
\providecommand{\OperatorTok}[1]{\textcolor[rgb]{0.40,0.40,0.40}{#1}}
\providecommand{\OtherTok}[1]{\textcolor[rgb]{0.00,0.44,0.13}{#1}}
\providecommand{\PreprocessorTok}[1]{\textcolor[rgb]{0.74,0.48,0.00}{#1}}
\providecommand{\RegionMarkerTok}[1]{#1}
\providecommand{\SpecialCharTok}[1]{\textcolor[rgb]{0.25,0.44,0.63}{#1}}
\providecommand{\SpecialStringTok}[1]{\textcolor[rgb]{0.25,0.44,0.63}{#1}}
\providecommand{\StringTok}[1]{\textcolor[rgb]{0.25,0.44,0.63}{#1}}
\providecommand{\VariableTok}[1]{\textcolor[rgb]{0.10,0.09,0.49}{#1}}
\providecommand{\VerbatimStringTok}[1]{\textcolor[rgb]{0.25,0.44,0.63}{#1}}
\providecommand{\WarningTok}[1]{\textcolor[rgb]{0.38,0.63,0.69}{\textbf{\textit{#1}}}}

% ═══════ IMAGE SCALING ═══════
\makeatletter
\def\maxwidth{\ifdim\Gin@nat@width>\linewidth\linewidth\else\Gin@nat@width\fi}
\def\maxheight{\ifdim\Gin@nat@height>0.8\textheight 0.8\textheight\else\Gin@nat@height\fi}
\makeatother
\setkeys{Gin}{width=\maxwidth,height=\maxheight,keepaspectratio}

\hypersetup{
  colorlinks=true,
  linkcolor=blue,
  urlcolor=blue,
  unicode=true,
  pdfencoding=auto,
  bookmarksnumbered=true
}

\setlength{\parskip}{6pt plus 2pt minus 1pt}
\setlength{\parindent}{0pt}
\setlength{\emergencystretch}{3em}

\begin{document}

$if(title)$
\title{$title$}
$endif$
$if(author)$
\author{$for(author)$$author$$sep$ \and $endfor$}
$endif$
\date{}
$if(title)$
\maketitle
$endif$

$if(toc)$
{
\hypersetup{linkcolor=black}
\setcounter{tocdepth}{$if(toc-depth)$$toc-depth$$else$3$endif$}
\tableofcontents
}
\clearpage
$endif$

$body$

\end{document}
`

	replacer := strings.NewReplacer(
		"<<MAINFONT>>", fc.MainFont,
		"<<MONOFONT>>", fc.MonoFont,
		"<<CJKMAINFONT>>", fc.CJKMainFont,
		"<<CJKFALLBACK>>", fc.CJKFallback,
	)
	return replacer.Replace(template)
}

func (a *App) ensureLaTeXPackages() {
	required := []string{
		"fvextra", "framed", "booktabs",
		"longtable", "xcolor", "etoolbox",
		"fontspec", "xeCJK", "luatexja",
		"geometry", "graphicx", "hyperref",
		"amsmath", "amssymb", "luacode",
	}

	var missing []string
	for _, pkg := range required {
		cmd := exec.Command("kpsewhich", pkg+".sty")
		if output, err := cmd.Output(); err != nil || len(strings.TrimSpace(string(output))) == 0 {
			missing = append(missing, pkg)
		}
	}

	if len(missing) == 0 {
		a.log("✅ LaTeX 依赖检查通过")
		return
	}

	a.log(fmt.Sprintf("⚠️  缺失 LaTeX 包: %s", strings.Join(missing, ", ")))

	if _, err := exec.LookPath("tlmgr"); err != nil {
		a.log("❌ tlmgr 不可用，请手动安装: tlmgr install " + strings.Join(missing, " "))
		return
	}

	for _, pkg := range missing {
		a.log(fmt.Sprintf("📦 安装 %s...", pkg))
		cmd := exec.Command("tlmgr", "install", pkg)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			a.log(fmt.Sprintf("⚠️  %s 安装失败: %s", pkg, strings.TrimSpace(stderr.String())))
		} else {
			a.log(fmt.Sprintf("✅ %s 已安装", pkg))
		}
	}
}

// ============================================================================
// Markdown generation
// ============================================================================

func (a *App) toMarkdown(inputEpub, outputMd string) error {
	mediaDir := filepath.Join(filepath.Dir(outputMd), "media")

	args := []string{
		inputEpub,
		"-o", outputMd,
		"-t", "gfm",
		"--wrap=none",
		"--extract-media=" + mediaDir,
		"--standalone",
	}

	a.log(fmt.Sprintf("🔧 Markdown: %s", strings.Join(args, " ")))

	if err := a.runPandoc(args); err != nil {
		return err
	}

	return a.cleanMarkdown(outputMd)
}

func (a *App) cleanMarkdown(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	content := string(data)
	content = reBlankMD.ReplaceAllString(content, "\n\n")
	content = reDivMD.ReplaceAllString(content, "")
	content = reSpanMD.ReplaceAllString(content, "")

	header := fmt.Sprintf("<!-- Athanor V4.3 | Generated: %s -->\n\n",
		time.Now().Format("2006-01-02 15:04:05"))
	content = header + strings.TrimSpace(content) + "\n"

	return os.WriteFile(path, []byte(content), 0644)
}

// ============================================================================
// Pandoc executor
// ============================================================================

// runPandoc executes Pandoc with the given arguments. The process context is
// derived from a.ctx so it is killed on app shutdown. On non-zero exit, the
// function no longer silently returns nil just because an output file > 1KB
// exists — it also checks the exit code severity and logs stderr details.
func (a *App) runPandoc(args []string, jobID ...string) error {
	ctx, cancel := context.WithTimeout(a.ctx, PandocTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "pandoc", args...)
	hideCmdWindow(cmd)

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动失败: %w", err)
	}

	jid := ""
	if len(jobID) > 0 {
		jid = jobID[0]
	}

	var stderrBuf bytes.Buffer
	done := make(chan struct{})
	pageRe := regexp.MustCompile(`\[(\d+)`)
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		lastPage := 0
		lastLogTime := time.Now()
		for {
			n, readErr := stderrPipe.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				stderrBuf.WriteString(chunk)

				matches := pageRe.FindAllStringSubmatch(chunk, -1)
				for _, m := range matches {
					if len(m) > 1 {
						page := 0
						fmt.Sscanf(m[1], "%d", &page)
						if page > lastPage+20 || time.Since(lastLogTime) > 5*time.Second {
							msg := fmt.Sprintf("📄 渲染中... 第 %d 页", page)
							a.log(msg)
							if jid != "" {
								pct := 70.0 + float64(page%1000)/1000.0*25.0
								if pct > 95 {
									pct = 95
								}
								a.progress(jid, "pdf", pct, msg)
							}
							lastPage = page
							lastLogTime = time.Now()
						}
					}
				}
			}
			if readErr != nil {
				break
			}
		}
	}()

	waitErr := cmd.Wait()
	<-done

	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("超时 (%v)", PandocTimeout)
	}

	// If app is shutting down, abort.
	if a.ctx.Err() != nil {
		return fmt.Errorf("应用关闭，Pandoc 中止")
	}

	if waitErr != nil {
		stderrStr := stderrBuf.String()
		numErrors := countErrors(stderrStr)

		// Check if output file was produced despite the error.
		outputPdf := extractOutputPath(args)
		if outputPdf != "" {
			if info, statErr := os.Stat(outputPdf); statErr == nil && info.Size() > 1024 {
				// Determine exit code if possible.
				exitCode := 0
				if exitErr, ok := waitErr.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				}

				// Exit code 1 with a real output file is typically just warnings.
				// Exit code >= 2 indicates a real failure — do not silently swallow.
				if exitCode <= 1 {
					a.log(fmt.Sprintf("⚠️  Pandoc 退出码 %d, %d 个非致命错误, 但输出已生成 (%.2f MB)",
						exitCode, numErrors, float64(info.Size())/1024/1024))
					// Log the tail of stderr so the user can investigate if needed.
					if len(stderrStr) > 0 {
						tail := stderrStr
						if len(tail) > 500 {
							tail = tail[len(tail)-500:]
						}
						a.log(fmt.Sprintf("⚠️  Pandoc stderr (尾部):\n%s", tail))
					}
					return nil
				}

				// exitCode >= 2: real error even though a file was produced.
				a.log(fmt.Sprintf("❌ Pandoc 退出码 %d — 即使有输出文件也视为失败", exitCode))
			}
		}

		if len(stderrStr) > 1500 {
			stderrStr = stderrStr[len(stderrStr)-1500:]
		}
		a.log(fmt.Sprintf("❌ Pandoc stderr:\n%s", stderrStr))
		return fmt.Errorf("pandoc: %w", waitErr)
	}

	return nil
}

func extractOutputPath(args []string) string {
	for i, arg := range args {
		if arg == "-o" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func countErrors(stderr string) int {
	count := 0
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "!") {
			count++
		}
	}
	return count
}

// ============================================================================
// Utilities
// ============================================================================

func isImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tiff", ".tif", ".webp":
		return true
	}
	return false
}

func outputPath(input, format string) string {
	dir := filepath.Dir(input)
	name := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	return filepath.Join(dir, name+"_athanor."+format)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (a *App) fail(jobID, msg string) ConversionProgress {
	a.log("❌ " + msg)
	return ConversionProgress{
		JobID:      jobID,
		Stage:      "error",
		IsError:    true,
		IsComplete: true,
		Message:    msg,
	}
}

func (a *App) progress(jobID, stage string, pct float64, msg string) {
	a.log(msg)
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "conversion:progress", ConversionProgress{
			JobID:    jobID,
			Stage:    stage,
			Progress: pct,
			Message:  msg,
		})
	}
}

func (a *App) printSanitizeStats(reports []SanitizationReport) {
	total := len(reports)
	counts := map[string]int{}
	fastCount := 0
	for _, r := range reports {
		counts[r.Status]++
		for _, act := range r.Actions {
			if strings.HasPrefix(act, "FAST_") {
				fastCount++
				break
			}
		}
	}

	a.log("╔════════════════════════════════════════════════════╗")
	a.log(fmt.Sprintf("║  图像净化报告: %d 个文件                          ║", total))
	a.log("╠════════════════════════════════════════════════════╣")
	a.log(fmt.Sprintf("║  ✅ 正常: %4d │ 🔧 修复: %4d │ ❌ 失败: %4d    ║",
		counts["OK"], counts["REPAIRED"]+counts["REPLACED"], counts["FAILED"]))
	a.log(fmt.Sprintf("║  ⚡ 快速路径: %4d (跳过 decode/re-encode)        ║", fastCount))
	a.log("╚════════════════════════════════════════════════════╝")
}

// prewarmFontCache runs luaotfload-tool --update before compilation so that
// the first LuaLaTeX pass does not stall on a cold font cache.
func (a *App) prewarmFontCache(engine string) {
	if engine != "lualatex" {
		return
	}

	toolPath, err := exec.LookPath("luaotfload-tool")
	if err != nil {
		a.log("⚠️  luaotfload-tool 不可用，跳过字体缓存预热")
		return
	}

	a.log("🔤 预热 LuaLaTeX 字体缓存...")
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, toolPath, "--update", "--force")
	hideCmdWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		a.log(fmt.Sprintf("⚠️  字体缓存预热失败 (非致命): %v", err))
	} else {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(lines) > 0 {
			a.log(fmt.Sprintf("✅ 字体缓存就绪: %s", lines[len(lines)-1]))
		}
	}
}
