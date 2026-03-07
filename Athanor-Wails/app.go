package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"Athanor-Wails/internal/rag"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const maxLogLines = 10000

type App struct {
	ctx       context.Context
	ctxCancel context.CancelFunc

	mu        sync.RWMutex
	logBuffer []string
	logSeq    int

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
}

func NewApp() *App {
	return &App{
		logBuffer: make([]string, 0, 2000),
	}
}

func (a *App) startup(ctx context.Context) {
	derivedCtx, cancel := context.WithCancel(ctx)
	a.ctx = derivedCtx
	a.ctxCancel = cancel

	a.log("Athanor RAG Edition")
	a.log("Target: EPUB -> RAG Markdown")
}

func (a *App) Shutdown(ctx context.Context) {
	a.log("Application shutdown")
	if a.ctxCancel != nil {
		a.ctxCancel()
	}
}

func (a *App) log(msg string) {
	a.mu.Lock()
	ts := time.Now().Format("15:04:05.000")
	line := fmt.Sprintf("[%s] %s", ts, msg)

	if len(a.logBuffer) >= maxLogLines {
		a.logBuffer = a.logBuffer[maxLogLines/5:]
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

func (a *App) GetLogsSince(since int) map[string]interface{} {
	a.mu.RLock()
	defer a.mu.RUnlock()

	total := a.logSeq
	bufLen := len(a.logBuffer)
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
		a.log("User cancelled file selection")
		return "", nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("无法访问文件: %w", err)
	}
	if info.IsDir() || info.Size() == 0 {
		return "", fmt.Errorf("无效文件")
	}

	a.log(fmt.Sprintf("Selected: %s (%.2f MB)", filepath.Base(path), float64(info.Size())/1024/1024))
	return path, nil
}

func (a *App) ConvertBook(inputPath string, outputFormat string) ConversionProgress {
	if !a.isProcessing.CompareAndSwap(false, true) {
		return a.fail("", "系统忙，请等待当前任务完成")
	}
	defer a.isProcessing.Store(false)

	jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())
	a.currentJobID.Store(jobID)

	inputInfo, err := os.Stat(inputPath)
	if err != nil {
		return a.fail(jobID, fmt.Sprintf("文件不可访问: %v", err))
	}
	if !strings.HasSuffix(strings.ToLower(inputPath), ".epub") {
		return a.fail(jobID, "仅支持 EPUB 文件")
	}

	a.progress(jobID, "init", 0, "初始化转换")
	a.log(fmt.Sprintf("Input: %s (%.2f MB)", filepath.Base(inputPath), float64(inputInfo.Size())/1024/1024))

	options := rag.Options{
		OutputRootDir: filepath.Dir(inputPath),
		BaseName:      outputPathBase(inputPath),
		Logger:        a.log,
		Progress: func(stage string, pct float64, message string) {
			a.progress(jobID, stage, pct, message)
		},
	}

	result, err := rag.ConvertEPUB(a.ctx, inputPath, options)
	if err != nil {
		return a.fail(jobID, err.Error())
	}

	a.log(fmt.Sprintf("Markdown: %s", result.MainMarkdownPath))
	if result.DebugMarkdownPath != "" {
		a.log(fmt.Sprintf("Debug Markdown: %s", result.DebugMarkdownPath))
	}
	a.log(fmt.Sprintf("Chapters: %s", filepath.Join(result.ArtifactDir, "chapters")))
	a.log(fmt.Sprintf("Metadata: %s", result.MetadataPath))
	a.log(fmt.Sprintf("TOC: %s", result.TOCPath))
	a.log(fmt.Sprintf("Chunks: %s", result.ChunksPath))
	a.log(fmt.Sprintf("Diagnostics: %s", result.DiagnosticsPath))

	if summary, err := json.MarshalIndent(result.Stats, "", "  "); err == nil {
		a.log("Stats:")
		for _, line := range strings.Split(string(summary), "\n") {
			a.log("  " + line)
		}
	}

	a.progress(jobID, "complete", 100, "转换完成")
	return ConversionProgress{
		JobID:        jobID,
		Stage:        "complete",
		Progress:     100,
		IsComplete:   true,
		Message:      "转换成功",
		OutputPath:   result.MainMarkdownPath,
		MarkdownPath: result.MainMarkdownPath,
	}
}

func outputPathBase(input string) string {
	name := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	name = strings.TrimSpace(strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	).Replace(name))
	if name == "" {
		name = "book"
	}
	return name + "_athanor"
}

func (a *App) fail(jobID, msg string) ConversionProgress {
	a.log("ERROR: " + msg)

	if a.ctx != nil && jobID != "" {
		wailsRuntime.EventsEmit(a.ctx, "conversion:progress", ConversionProgress{
			JobID:      jobID,
			Stage:      "error",
			Progress:   0,
			Message:    msg,
			IsError:    true,
			IsComplete: true,
		})
	}

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
