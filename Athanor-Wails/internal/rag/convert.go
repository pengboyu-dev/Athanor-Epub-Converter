package rag

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func ConvertEPUB(ctx context.Context, inputPath string, options Options) (ConvertResult, error) {
	if ctx == nil {
		ctx = options.Context
	}
	if ctx == nil {
		ctx = context.Background()
	}

	logf := options.Logger
	if logf == nil {
		logf = func(string) {}
	}
	progress := options.Progress
	if progress == nil {
		progress = func(string, float64, string) {}
	}

	progress("inspect", 5, "📦 读取 EPUB 容器...")
	book, err := ParseEPUB(ctx, inputPath)
	if err != nil {
		return ConvertResult{}, err
	}
	book.Metadata.SourcePath = inputPath

	hash, err := fileSHA256(inputPath)
	if err != nil {
		return ConvertResult{}, fmt.Errorf("计算文件指纹失败: %w", err)
	}
	book.Metadata.SourceSHA256 = hash

	progress("normalize", 30, "🧹 清洗结构并生成文档模型...")
	NormalizeBook(&book)
	logf(fmt.Sprintf("📚 正文章节: %d | 前后置材料: %d", len(book.Main), len(book.Back)))

	progress("render", 65, "📝 渲染 Markdown...")
	mainMD := RenderBookMarkdown(book)
	debugMD := RenderDebugMarkdown(book)
	chapterDocs := RenderChapterMarkdown(book)
	chunks := BuildChunks(book, options.ChunkConfig)
	book.Stats.ChunkCount = len(chunks)
	diagnostics := BuildDiagnostics(book, chunks, options.ChunkConfig)

	progress("write", 85, "💾 写出主文档与章节文件...")
	mainPath, debugPath, artifactDir, err := writeArtifacts(options, book, mainMD, debugMD, chapterDocs, chunks, diagnostics)
	if err != nil {
		return ConvertResult{}, err
	}

	progress("complete", 100, "✅ 输出已生成")
	return ConvertResult{
		MainMarkdownPath:  mainPath,
		DebugMarkdownPath: debugPath,
		ArtifactDir:       artifactDir,
		MetadataPath:      filepath.Join(artifactDir, "metadata.json"),
		TOCPath:           filepath.Join(artifactDir, "toc.json"),
		ChunksPath:        filepath.Join(artifactDir, "chunks.jsonl"),
		DiagnosticsPath:   filepath.Join(artifactDir, "diagnostics.json"),
		Stats:             book.Stats,
	}, nil
}

func writeArtifacts(options Options, book Book, mainMD string, debugMD string, chapterDocs map[string]string, chunks []Chunk, diagnostics Diagnostics) (string, string, string, error) {
	mainPath := filepath.Join(options.OutputRootDir, options.BaseName+".md")
	artifactDir := filepath.Join(options.OutputRootDir, options.BaseName)
	chaptersDir := filepath.Join(artifactDir, "chapters")
	debugPath := filepath.Join(artifactDir, "debug.md")

	if err := os.MkdirAll(chaptersDir, 0o755); err != nil {
		return "", "", "", fmt.Errorf("创建输出目录失败: %w", err)
	}
	if err := os.WriteFile(mainPath, []byte(mainMD), 0o644); err != nil {
		return "", "", "", fmt.Errorf("写入主 Markdown 失败: %w", err)
	}
	if err := os.WriteFile(debugPath, []byte(debugMD), 0o644); err != nil {
		return "", "", "", fmt.Errorf("写入 debug markdown 失败: %w", err)
	}

	for id, content := range chapterDocs {
		filename := filepath.Join(chaptersDir, sanitizePathComponent(id)+".md")
		if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
			return "", "", "", fmt.Errorf("写入章节 Markdown 失败: %w", err)
		}
	}

	toc := make([]TOCItem, 0, len(book.Main)+len(book.Back))
	for _, chapter := range append(append([]Chapter(nil), book.Main...), book.Back...) {
		toc = append(toc, TOCItem{
			ID:             chapter.ID,
			Title:          chapter.Title,
			Kind:           chapter.Kind,
			ClassifyReason: chapter.ClassifyReason,
			Order:          chapter.Order,
			Source:         chapter.SourceRef,
		})
	}

	if err := writeJSON(filepath.Join(artifactDir, "metadata.json"), book.Metadata); err != nil {
		return "", "", "", err
	}
	if err := writeJSON(filepath.Join(artifactDir, "toc.json"), toc); err != nil {
		return "", "", "", err
	}
	if err := writeJSON(filepath.Join(artifactDir, "stats.json"), book.Stats); err != nil {
		return "", "", "", err
	}
	if err := writeJSON(filepath.Join(artifactDir, "diagnostics.json"), diagnostics); err != nil {
		return "", "", "", err
	}
	if err := writeJSONL(filepath.Join(artifactDir, "chunks.jsonl"), chunks); err != nil {
		return "", "", "", err
	}

	return mainPath, debugPath, artifactDir, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 JSON 失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", filepath.Base(path), err)
	}
	return nil
}

func writeJSONL(path string, chunks []Chunk) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("写入 chunks.jsonl 失败: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for _, chunk := range chunks {
		line, err := json.Marshal(chunk)
		if err != nil {
			return fmt.Errorf("序列化 chunk 失败: %w", err)
		}
		if _, err := writer.Write(line); err != nil {
			return fmt.Errorf("写入 chunk 失败: %w", err)
		}
		if err := writer.WriteByte('\n'); err != nil {
			return fmt.Errorf("写入换行失败: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("刷新 chunks.jsonl 失败: %w", err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	sum := hash.Sum(nil)
	return hex.EncodeToString(sum[:]), nil
}

func sanitizePathComponent(s string) string {
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	return strings.TrimSpace(replacer.Replace(s))
}
