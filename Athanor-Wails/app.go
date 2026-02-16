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
// 1. CONSTANTS & CONFIGURATION
// ============================================================================

const (
	MaxImageDimension   = 50000
	MaxPixelCount       = 500_000_000       // 500 megapixels
	MaxDecompressedSize = 500 * 1024 * 1024 // 500MB per image decode
	MaxLogLines         = 10000
	PandocTimeout       = 20 * time.Minute
	StreamBufferSize    = 64 * 1024 // 64KB IO buffer
	TargetDPI           = 96
	JPEGQuality         = 95
)

// ============================================================================
// 2. CORE TYPES
// ============================================================================

type App struct {
	ctx          context.Context
	mu           sync.RWMutex
	logBuffer    []string
	currentJobID atomic.Value
	isProcessing atomic.Bool
}

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

type SanitizationReport struct {
	FilePath       string   `json:"filePath"`
	OriginalFormat string   `json:"originalFormat"`
	Actions        []string `json:"actions"`
	Status         string   `json:"status"`
	Error          string   `json:"error,omitempty"`
	FileSizeBefore int64    `json:"fileSizeBefore"`
	FileSizeAfter  int64    `json:"fileSizeAfter"`
}

type FontConfig struct {
	MainFont    string
	CJKMainFont string
	CJKFallback string
	MonoFont    string
}

// ============================================================================
// 3. LIFECYCLE
// ============================================================================

func NewApp() *App {
	return &App{
		logBuffer: make([]string, 0, 2000),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.log("🔥 ATHANOR V4.0 FINAL — Dual Output Edition")
	a.log(fmt.Sprintf("⚙️  Platform: %s/%s | CPUs: %d", runtime.GOOS, runtime.GOARCH, runtime.NumCPU()))
	a.log("🛡️  Protocols: MonsterKiller | DPI-Injector | ①②③-Fix | AI-Markdown")
	a.log("════════════════════════════════════════════════════════════════")
}

func (a *App) log(msg string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ts := time.Now().Format("15:04:05.000")
	line := fmt.Sprintf("[%s] %s", ts, msg)

	if len(a.logBuffer) >= MaxLogLines {
		a.logBuffer = a.logBuffer[MaxLogLines/5:]
	}
	a.logBuffer = append(a.logBuffer, line)
	fmt.Println(line)

	go a.emitSafe(msg)
}

func (a *App) emitSafe(msg string) {
	if a.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(a.ctx, "log", "INFO||"+msg)
}

func (a *App) GetLogs() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, len(a.logBuffer))
	copy(out, a.logBuffer)
	return out
}

// ============================================================================
// 4. FILE SELECTION
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
// 5. MAIN ORCHESTRATOR
// ============================================================================

func (a *App) ConvertBook(inputPath string, outputFormat string) ConversionProgress {
	if !a.isProcessing.CompareAndSwap(false, true) {
		return a.fail("", "系统忙，请等待当前任务完成")
	}
	defer a.isProcessing.Store(false)

	jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())
	a.currentJobID.Store(jobID)
	result := ConversionProgress{JobID: jobID}

	// 解析输出模式
	fmtLower := strings.ToLower(outputFormat)
	wantPDF := strings.Contains(fmtLower, "pdf") || strings.Contains(fmtLower, "both") || strings.Contains(fmtLower, "all")
	wantMD := strings.Contains(fmtLower, "md") || strings.Contains(fmtLower, "markdown") || strings.Contains(fmtLower, "both") || strings.Contains(fmtLower, "all")
	if !wantPDF && !wantMD {
		wantPDF = true
	}

	a.progress(jobID, "init", 0, "🚀 初始化转换管道...")
	a.log(fmt.Sprintf("📤 输出模式: PDF=%v, Markdown=%v", wantPDF, wantMD))

	// ── 1. 验证输入 ───────────────────────────────────────────────────────
	inputInfo, err := os.Stat(inputPath)
	if err != nil {
		return a.fail(jobID, fmt.Sprintf("文件不可访问: %v", err))
	}
	if !strings.HasSuffix(strings.ToLower(inputPath), ".epub") {
		return a.fail(jobID, "仅支持 EPUB 文件")
	}
	a.log(fmt.Sprintf("📖 输入: %s (%.2f MB)", filepath.Base(inputPath), float64(inputInfo.Size())/1024/1024))

	// ── 2. 工作空间 ──────────────────────────────────────────────────────
	a.progress(jobID, "workspace", 5, "🏗️  创建隔离环境...")
	workDir, err := os.MkdirTemp("", "athanor_v4_*")
	if err != nil {
		return a.fail(jobID, fmt.Sprintf("工作空间失败: %v", err))
	}
	defer func() {
		a.log("🧹 清理工作空间...")
		os.RemoveAll(workDir)
	}()

	// ── 3. 解压 ──────────────────────────────────────────────────────────
	a.progress(jobID, "unpack", 10, "📦 解压 EPUB...")
	unpackDir := filepath.Join(workDir, "unpacked")
	if err := a.unzipStreaming(inputPath, unpackDir); err != nil {
		return a.fail(jobID, fmt.Sprintf("解压失败: %v", err))
	}

	// ── 4. 图像洗白 ─────────────────────────────────────────────────────
	a.progress(jobID, "sanitize", 20, "🧼 深度图像净化...")
	reports, err := a.sanitizeAllImages(unpackDir)
	if err != nil {
		return a.fail(jobID, fmt.Sprintf("净化失败: %v", err))
	}
	a.printSanitizeStats(reports)

	// ── 5. 重建 EPUB ────────────────────────────────────────────────────
	a.progress(jobID, "repack", 45, "📦 重建 EPUB (OCF 合规)...")
	cleanEpub := filepath.Join(workDir, "sanitized.epub")
	if err := a.zipEPUBStrict(unpackDir, cleanEpub); err != nil {
		return a.fail(jobID, fmt.Sprintf("重建失败: %v", err))
	}

	// ── 6. Markdown（给 AI 读）─────────────────────────────────────────
	if wantMD {
		a.progress(jobID, "markdown", 55, "📝 生成 AI-Optimized Markdown...")
		mdPath := outputPath(inputPath, "md")
		if err := a.toMarkdown(cleanEpub, mdPath); err != nil {
			a.log(fmt.Sprintf("⚠️  Markdown 失败 (非致命): %v", err))
		} else {
			result.MarkdownPath = mdPath
			a.log(fmt.Sprintf("✅ Markdown: %s", mdPath))
		}
	}

	// ── 7. PDF（给人类读）───────────────────────────────────────────────
	if wantPDF {
		a.progress(jobID, "pdf", 70, "📄 PDF 渲染 (XeLaTeX + ①②③ 修复)...")
		pdfPath := outputPath(inputPath, "pdf")
		if err := a.toPDF(cleanEpub, pdfPath, workDir); err != nil {
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

	// ── 8. 完成 ──────────────────────────────────────────────────────────
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
// 6. IMAGE SANITIZATION ENGINE
// ============================================================================

func (a *App) sanitizeAllImages(dir string) ([]SanitizationReport, error) {
	var reports []SanitizationReport
	count := 0

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			a.log(fmt.Sprintf("⚠️  跳过 %s: %v", path, err))
			return nil
		}
		if d.IsDir() || !isImageExt(filepath.Ext(path)) {
			return nil
		}

		count++
		rel, _ := filepath.Rel(dir, path)
		a.log(fmt.Sprintf("🧼 [%d] %s", count, rel))

		r := a.sanitizeOne(path)
		reports = append(reports, r)

		if r.Status != "OK" {
			a.log(fmt.Sprintf("   ↳ %s: %s", r.Status, strings.Join(r.Actions, " | ")))
		}
		return nil
	})

	a.log(fmt.Sprintf("✨ 扫描完成: %d 个图像", count))
	return reports, err
}

func (a *App) sanitizeOne(path string) SanitizationReport {
	r := SanitizationReport{FilePath: path, Actions: make([]string, 0, 8), Status: "OK"}

	if info, err := os.Stat(path); err == nil {
		r.FileSizeBefore = info.Size()
	}

	// 1. 格式嗅探
	realFmt, err := sniffFormat(path)
	if err != nil {
		r.Status = "FAILED"
		r.Error = err.Error()
		a.placeholder(path)
		r.Actions = append(r.Actions, "INVALID_REPLACED")
		return r
	}
	r.OriginalFormat = realFmt

	// 检测格式欺骗
	extFmt := extToFormat(filepath.Ext(path))
	if extFmt != "" && extFmt != realFmt {
		a.log(fmt.Sprintf("   ⚠️  格式欺骗: 扩展名=%s 实际=%s", extFmt, realFmt))
		r.Actions = append(r.Actions, fmt.Sprintf("SPOOF_%s→%s", extFmt, realFmt))
	}

	// 2. 安全解码
	img, err := decodeSafe(path, realFmt)
	if err != nil {
		r.Status = "REPLACED"
		r.Error = err.Error()
		a.placeholder(path)
		r.Actions = append(r.Actions, "DECODE_FAIL_REPLACED")
		return r
	}

	// 3. EXIF 旋转 + 剥离
	if rotated, act := exifRotate(path, img); act != "" {
		img = rotated
		r.Actions = append(r.Actions, act)
	}

	// 4. 色彩空间标准化
	if normalized, act := toRGB(img); act != "" {
		img = normalized
		r.Actions = append(r.Actions, act)
	}

	// 5. 透明度压平
	if flat, act := flattenAlpha(img); act != "" {
		img = flat
		r.Actions = append(r.Actions, act)
	}

	// 6. 重编码 + DPI 注入
	ext := strings.ToLower(filepath.Ext(path))
	if err := a.reencode(path, img, ext); err != nil {
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

// ============================================================================
// 7. IMAGE PRIMITIVES
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

func decodeSafe(path, format string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

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

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("invalid dimensions: %dx%d", w, h)
	}
	if w > MaxImageDimension || h > MaxImageDimension {
		return nil, fmt.Errorf("monster image: %dx%d > %d", w, h, MaxImageDimension)
	}
	if int64(w)*int64(h) > MaxPixelCount {
		return nil, fmt.Errorf("pixel bomb: %dM pixels", int64(w)*int64(h)/1_000_000)
	}

	return img, nil
}

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

func flattenAlpha(img image.Image) (image.Image, string) {
	hasAlpha := false
	switch img.(type) {
	case *image.NRGBA, *image.RGBA, *image.RGBA64, *image.NRGBA64:
		hasAlpha = true
	}
	if !hasAlpha {
		return img, ""
	}

	bounds := img.Bounds()
	transparent := false

	// 采样检测（大图加速）
	step := 1
	if bounds.Dx()*bounds.Dy() > 1_000_000 {
		step = 10
	}
	for y := bounds.Min.Y; y < bounds.Max.Y && !transparent; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			_, _, _, a := img.At(x, y).RGBA()
			if a < 65535 {
				transparent = true
				break
			}
		}
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
// 8. DPI-AWARE RE-ENCODING (The 1-DPI Killer)
// ============================================================================

func (a *App) reencode(path string, img image.Image, ext string) error {
	tmpPath := path + ".athanor_tmp"

	switch ext {
	case ".png":
		if err := savePNGWithDPI(tmpPath, img); err != nil {
			return err
		}
	default:
		// JPEG for everything else (strips metadata, forces baseline)
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

	// 编码到缓冲区
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return err
	}

	// 注入 96 DPI 到 JFIF 头
	data := injectJFIFDPI(buf.Bytes(), TargetDPI)

	_, err = f.Write(data)
	return err
}

// injectJFIFDPI 在 JPEG 字节流中强制设置 DPI
// 如果已有 JFIF APP0 段则修改，否则插入新段
func injectJFIFDPI(data []byte, dpi int) []byte {
	if len(data) < 20 {
		return data
	}

	// 搜索已有的 JFIF APP0 (FF E0)
	for i := 2; i < len(data)-16; i++ {
		if data[i] == 0xFF && data[i+1] == 0xE0 {
			// 验证 JFIF 标识
			if i+9 <= len(data) && string(data[i+4:i+9]) == "JFIF\x00" {
				// 修改已有 JFIF 段的 DPI
				data[i+11] = 0x01                                        // units = DPI
				binary.BigEndian.PutUint16(data[i+12:i+14], uint16(dpi)) // X density
				binary.BigEndian.PutUint16(data[i+14:i+16], uint16(dpi)) // Y density
				return data
			}
		}
	}

	// 没找到 JFIF，在 SOI (FF D8) 后插入新的 APP0 段
	jfif := []byte{
		0xFF, 0xE0, // APP0 marker
		0x00, 0x10, // length = 16
		'J', 'F', 'I', 'F', 0x00, // identifier
		0x01, 0x01, // version 1.1
		0x01,                      // units = DPI
		byte(dpi >> 8), byte(dpi), // X density
		byte(dpi >> 8), byte(dpi), // Y density
		0x00, 0x00, // thumbnail size
	}

	result := make([]byte, 0, len(data)+len(jfif))
	result = append(result, data[:2]...) // SOI (FF D8)
	result = append(result, jfif...)     // new JFIF APP0
	result = append(result, data[2:]...) // rest of file
	return result
}

func savePNGWithDPI(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// 编码到缓冲区
	var buf bytes.Buffer
	enc := &png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return err
	}

	// 注入 pHYs chunk（96 DPI = 3780 pixels/meter）
	data := injectPNGpHYs(buf.Bytes(), TargetDPI)

	_, err = f.Write(data)
	return err
}

// injectPNGpHYs 在 PNG 的 IHDR 后插入 pHYs 物理尺寸 chunk
func injectPNGpHYs(data []byte, dpi int) []byte {
	// PNG 结构: 8-byte signature + chunks
	// 每个 chunk: 4B length + 4B type + data + 4B CRC
	if len(data) < 33 {
		return data
	}

	// 检查是否已有 pHYs
	if bytes.Contains(data, []byte("pHYs")) {
		// 找到并修改已有的 pHYs
		idx := bytes.Index(data, []byte("pHYs"))
		if idx > 0 && idx+13 <= len(data) {
			ppm := uint32(float64(dpi) / 0.0254)                // DPI -> pixels per meter
			binary.BigEndian.PutUint32(data[idx+4:idx+8], ppm)  // X
			binary.BigEndian.PutUint32(data[idx+8:idx+12], ppm) // Y
			data[idx+12] = 1                                    // unit = meter
			// 重算 CRC
			crc := crc32PNG(data[idx : idx+13])
			binary.BigEndian.PutUint32(data[idx+13:idx+17], crc)
		}
		return data
	}

	// 构造新的 pHYs chunk
	ppm := uint32(float64(dpi) / 0.0254) // 96 DPI ≈ 3780 ppm

	var phys bytes.Buffer
	// chunk data (9 bytes)
	chunkData := make([]byte, 9)
	binary.BigEndian.PutUint32(chunkData[0:4], ppm) // X pixels per unit
	binary.BigEndian.PutUint32(chunkData[4:8], ppm) // Y pixels per unit
	chunkData[8] = 1                                // unit = meter

	// length
	lengthBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBytes, 9)
	phys.Write(lengthBytes)

	// type + data
	typeAndData := append([]byte("pHYs"), chunkData...)
	phys.Write(typeAndData)

	// CRC over type+data
	crc := crc32PNG(typeAndData)
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc)
	phys.Write(crcBytes)

	// 插入到 IHDR 之后
	// PNG signature (8 bytes) + IHDR chunk
	// IHDR chunk: 4B length + 4B "IHDR" + 13B data + 4B CRC = 25 bytes
	ihdrEnd := 8 + 25 // position after IHDR

	if ihdrEnd > len(data) {
		return data
	}

	result := make([]byte, 0, len(data)+phys.Len())
	result = append(result, data[:ihdrEnd]...)
	result = append(result, phys.Bytes()...)
	result = append(result, data[ihdrEnd:]...)
	return result
}

// crc32PNG 计算 PNG chunk 的 CRC32 (使用 ISO 3309 多项式)
func crc32PNG(data []byte) uint32 {
	// PNG uses CRC-32/ISO-HDLC
	var table [256]uint32
	for i := 0; i < 256; i++ {
		c := uint32(i)
		for j := 0; j < 8; j++ {
			if c&1 != 0 {
				c = 0xEDB88320 ^ (c >> 1)
			} else {
				c >>= 1
			}
		}
		table[i] = c
	}

	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc = table[byte(crc)^b] ^ (crc >> 8)
	}
	return crc ^ 0xFFFFFFFF
}

func (a *App) placeholder(path string) {
	svg := `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="400" height="300">
  <rect width="400" height="300" fill="#f8f8f8"/>
  <rect x="10" y="10" width="380" height="280" fill="none" stroke="#ddd" stroke-width="2" stroke-dasharray="8,4"/>
  <text x="200" y="140" text-anchor="middle" font-family="sans-serif" font-size="16" fill="#999">⚠️ 损坏图像已移除</text>
  <text x="200" y="165" text-anchor="middle" font-family="sans-serif" font-size="11" fill="#bbb">Corrupted Image Removed</text>
</svg>`
	svgPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".svg"
	os.WriteFile(svgPath, []byte(svg), 0644)
	os.Remove(path)
}

// ============================================================================
// 9. EPUB CONTAINER OPERATIONS (Streaming)
// ============================================================================

func (a *App) unzipStreaming(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, zf := range r.File {
		fpath := filepath.Join(dest, zf.Name)

		// Zip Slip 防护
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

	// mimetype 必须排第一且不压缩 (EPUB OCF 规范)
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
		writer.Write(bytes.TrimSpace(mtData))
	}

	// 其余文件流式写入
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) == "mimetype" {
			return err
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
// 10. PDF GENERATION (带圈数字修复)
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

func (a *App) toPDF(inputEpub, outputPdf, workDir string) error {
	if _, err := exec.LookPath("pandoc"); err != nil {
		return fmt.Errorf("Pandoc 未安装")
	}

	a.ensureLaTeXPackages()

	fc := getFontConfig()
	a.log(fmt.Sprintf("🔤 字体: Main=%s CJK=%s Fallback=%s Mono=%s",
		fc.MainFont, fc.CJKMainFont, fc.CJKFallback, fc.MonoFont))

	// 从 Pandoc 导出默认模板
	templatePath := filepath.Join(workDir, "athanor_template.tex")
	defaultTemplate, err := a.getPandocDefaultTemplate()
	if err != nil {
		a.log(fmt.Sprintf("⚠️  无法获取默认模板，使用内置模板: %v", err))
		templateContent := buildFallbackTemplate(fc)
		if writeErr := os.WriteFile(templatePath, []byte(templateContent), 0644); writeErr != nil {
			return fmt.Errorf("模板写入失败: %w", writeErr)
		}
	} else {
		// 注入我们的字体配置和圈数字修复到默认模板
		patchedTemplate := patchDefaultTemplate(defaultTemplate, fc)
		if writeErr := os.WriteFile(templatePath, []byte(patchedTemplate), 0644); writeErr != nil {
			return fmt.Errorf("模板写入失败: %w", writeErr)
		}
	}

	a.log(fmt.Sprintf("📝 模板已就绪: %s", templatePath))

	args := []string{
		inputEpub,
		"-o", outputPdf,
		"--pdf-engine=xelatex",
		"--template=" + templatePath,
		"--toc",
		"--toc-depth=2",
		"-V", "geometry:margin=1in",
		"-V", fmt.Sprintf("mainfont=%s", fc.MainFont),
		"-V", fmt.Sprintf("monofont=%s", fc.MonoFont),
		"-V", fmt.Sprintf("CJKmainfont=%s", fc.CJKMainFont),
	}

	a.log(fmt.Sprintf("🔧 Pandoc: %s", strings.Join(args, " ")))
	return a.runPandoc(args)
}

// getPandocDefaultTemplate 从 Pandoc 导出其自带的 LaTeX 默认模板
func (a *App) getPandocDefaultTemplate() (string, error) {
	cmd := exec.Command("pandoc", "-D", "latex")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pandoc -D latex 失败: %w (%s)", err, stderr.String())
	}

	template := stdout.String()
	if len(template) < 100 {
		return "", fmt.Errorf("模板内容过短: %d bytes", len(template))
	}

	a.log(fmt.Sprintf("📄 Pandoc 默认模板: %d bytes", len(template)))
	return template, nil
}

// patchDefaultTemplate 将圈数字修复注入 Pandoc 默认模板
func patchDefaultTemplate(template string, fc FontConfig) string {
	circledNumbersFix := `
% ═══════ ATHANOR: CIRCLED NUMBERS FIX (①②③) ═══════
\xeCJKDeclareCharClass{CJK}{
  "2460 -> "24FF,
  "2600 -> "26FF,
  "2700 -> "27BF,
  "3200 -> "32FF
}
\setCJKfallbackfamilyfont{\CJKrmdefault}{` + fc.CJKFallback + `}
% ═══════ END ATHANOR PATCH ═══════
`

	// strings.Replace 找不到 marker 时安全返回原字符串，无需 if 判断
	template = strings.Replace(template, `\begin{document}`, circledNumbersFix+"\n"+`\begin{document}`, 1)

	return template
}

// buildFallbackTemplate 当无法获取 Pandoc 默认模板时使用的后备模板
func buildFallbackTemplate(fc FontConfig) string {
	template := `\documentclass[12pt,a4paper]{article}

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
\usepackage{fvextra}
\usepackage{framed}
\usepackage{soul}
\usepackage{calc}
\usepackage{etoolbox}

\geometry{a4paper, margin=1in}

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

% ═══════ PANDOC 3.x COMPATIBILITY ═══════
\providecommand{\pandocbounded}[1]{#1}
\providecommand{\tightlist}{%
  \setlength{\itemsep}{0pt}\setlength{\parskip}{0pt}}

\newlength{\cslhangindent}
\setlength{\cslhangindent}{1.5em}
\newlength{\csllabelwidth}
\setlength{\csllabelwidth}{3em}
\newenvironment{CSLReferences}[2]{}{}

% ═══════ SHADED CODE BLOCKS ═══════
\definecolor{shadecolor}{RGB}{245,245,245}
\newenvironment{Shaded}{\begin{snugshade}}{\end{snugshade}}

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
$if(date)$
\date{$date$}
$else$
\date{}
$endif$
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

// ensureLaTeXPackages 检测必需的 LaTeX 包并尝试自动安装缺失的
func (a *App) ensureLaTeXPackages() {
	required := []string{
		"fvextra", "framed", "lineno", "booktabs",
		"longtable", "xcolor", "etoolbox", "upquote",
		"fontspec", "xeCJK", "geometry", "graphicx",
		"hyperref",
	}

	// kpsewhich 映射：有些包名和 .sty 文件名不同
	styNames := map[string]string{
		"xeCJK":    "xeCJK",
		"graphicx": "graphicx",
	}

	var missing []string
	for _, pkg := range required {
		styName := pkg
		if mapped, ok := styNames[pkg]; ok {
			styName = mapped
		}

		cmd := exec.Command("kpsewhich", styName+".sty")
		if output, err := cmd.Output(); err != nil || len(strings.TrimSpace(string(output))) == 0 {
			missing = append(missing, pkg)
		}
	}

	if len(missing) == 0 {
		a.log("✅ LaTeX 依赖检查通过")
		return
	}

	a.log(fmt.Sprintf("⚠️  缺失 LaTeX 包: %s", strings.Join(missing, ", ")))

	// 尝试用 tlmgr 安装
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

// buildLaTeXTemplate 生成兼容新旧 xeCJK 的 LaTeX 模板
// 使用 CJK 类声明将带圈数字归入 CJK 字符类，
// 再用 setCJKfallbackfamilyfont 指定 Fallback 字体

func buildLaTeXTemplate(fc FontConfig) string {
	template := `\documentclass[12pt,a4paper]{article}

% ═══════ PACKAGES ═══════
\usepackage{fontspec}
\usepackage{xeCJK}
\usepackage{geometry}
\usepackage{graphicx}
\usepackage{hyperref}
\usepackage{longtable}
\usepackage{booktabs}
\usepackage{xcolor}
\usepackage{fvextra}
\usepackage{framed}

% ═══════ PAGE LAYOUT ═══════
\geometry{a4paper, margin=1in}

% ═══════ WESTERN FONTS ═══════
\setmainfont{<<MAINFONT>>}
\setmonofont{<<MONOFONT>>}[Scale=0.85]

% ═══════ CJK MAIN FONT ═══════
\setCJKmainfont{<<CJKMAINFONT>>}

% ═══════ CIRCLED NUMBERS FIX (①②③) ═══════
\xeCJKDeclareCharClass{CJK}{
  "2460 -> "24FF,
  "2600 -> "26FF,
  "2700 -> "27BF,
  "3200 -> "32FF
}
\setCJKfallbackfamilyfont{\CJKrmdefault}{<<CJKFALLBACK>>}

% ═══════ PANDOC 3.x COMPATIBILITY ═══════
\providecommand{\pandocbounded}[1]{#1}
\providecommand{\tightlist}{%
  \setlength{\itemsep}{0pt}\setlength{\parskip}{0pt}}

\newlength{\cslhangindent}
\setlength{\cslhangindent}{1.5em}
\newlength{\csllabelwidth}
\setlength{\csllabelwidth}{3em}
\newenvironment{CSLReferences}[2]{}{}

% ═══════ SHADED CODE BLOCKS ═══════
\definecolor{shadecolor}{RGB}{245,245,245}
\newenvironment{Shaded}{\begin{snugshade}}{\end{snugshade}}

% fvextra provides breaklines for Verbatim
\DefineVerbatimEnvironment{Highlighting}{Verbatim}{
  commandchars=\\\{\},
  fontsize=\small,
  baselinestretch=1.1
}

% ═══════ SYNTAX HIGHLIGHTING TOKENS ═══════
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

% ═══════ HYPERLINKS ═══════
\hypersetup{
  colorlinks=true,
  linkcolor=blue,
  urlcolor=blue,
  unicode=true,
  pdfencoding=auto,
  bookmarksnumbered=true
}

% ═══════ PARAGRAPH ═══════
\setlength{\parskip}{6pt plus 2pt minus 1pt}
\setlength{\parindent}{0pt}
\setlength{\emergencystretch}{3em}

% ═══════ DOCUMENT ═══════
\begin{document}

$if(title)$
\title{$title$}
$endif$
$if(author)$
\author{$for(author)$$author$$sep$ \and $endfor$}
$endif$
$if(date)$
\date{$date$}
$else$
\date{}
$endif$
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

// ============================================================================
// 11. MARKDOWN GENERATION (AI-Ready)
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

	// 移除多余空行
	reBlank := regexp.MustCompile(`\n{3,}`)
	content = reBlank.ReplaceAllString(content, "\n\n")

	// 移除残留 HTML 标签
	reDiv := regexp.MustCompile(`</?div[^>]*>`)
	content = reDiv.ReplaceAllString(content, "")
	reSpan := regexp.MustCompile(`</?span[^>]*>`)
	content = reSpan.ReplaceAllString(content, "")

	// 添加元信息
	header := fmt.Sprintf("<!-- Athanor V4.0 | Generated: %s -->\n\n",
		time.Now().Format("2006-01-02 15:04:05"))
	content = header + strings.TrimSpace(content) + "\n"

	return os.WriteFile(path, []byte(content), 0644)
}

// ============================================================================
// 12. PANDOC EXECUTOR
// ============================================================================

func (a *App) runPandoc(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), PandocTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "pandoc", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("超时 (%v)", PandocTimeout)
	}

	if err != nil {
		errStr := stderr.String()
		if len(errStr) > 1500 {
			errStr = errStr[:1500] + "\n...(truncated)"
		}
		a.log(fmt.Sprintf("❌ Pandoc stderr:\n%s", errStr))
		return fmt.Errorf("pandoc: %w", err)
	}

	if stdout.Len() > 0 {
		a.log(fmt.Sprintf("📤 Pandoc stdout: %s", stdout.String()))
	}

	return nil
}

// ============================================================================
// 13. UTILITIES
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
	for _, r := range reports {
		counts[r.Status]++
	}

	a.log("╔════════════════════════════════════════════════════╗")
	a.log(fmt.Sprintf("║  图像净化报告: %d 个文件                          ║", total))
	a.log("╠════════════════════════════════════════════════════╣")
	a.log(fmt.Sprintf("║  ✅ 正常: %4d │ 🔧 修复: %4d │ ❌ 失败: %4d    ║",
		counts["OK"], counts["REPAIRED"]+counts["REPLACED"], counts["FAILED"]))
	a.log("╚════════════════════════════════════════════════════╝")
}
