package rag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoldenFlowerSmoke(t *testing.T) {
	sample := filepath.Join("..", "..", "epub-raw", "The Secret of the Golden Flower The Classic Chinese Book of Life (Thomas Cleary) (Z-Library).epub")
	if _, err := os.Stat(sample); err != nil {
		t.Skip("sample epub not found")
	}

	result, err := ConvertEPUB(context.Background(), sample, Options{
		OutputRootDir: testOutputDir(t, "golden-flower"),
		BaseName:      "golden-flower",
	})
	if err != nil {
		t.Fatalf("ConvertEPUB failed: %v", err)
	}
	t.Logf("stats: %+v", result.Stats)

	if result.Stats.ChapterCount == 0 {
		t.Fatal("expected at least one main chapter")
	}
	if result.Stats.ChunkCount == 0 {
		t.Fatal("expected non-empty chunks")
	}

	mainData, err := os.ReadFile(result.MainMarkdownPath)
	if err != nil {
		t.Fatalf("read main markdown: %v", err)
	}
	if len(strings.TrimSpace(string(mainData))) < 500 {
		t.Fatalf("main markdown too short: %d", len(mainData))
	}
}
