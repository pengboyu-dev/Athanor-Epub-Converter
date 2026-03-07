package rag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type batchBaselineRecord struct {
	FileName            string   `json:"fileName"`
	Stats               Stats    `json:"stats"`
	ChapterTitles       []string `json:"chapterTitles"`
	ChunkFingerprints   []string `json:"chunkFingerprints"`
	ChunkCharacterStats []int    `json:"chunkCharacterStats,omitempty"`
}

func TestBatchRegressionBaseline(t *testing.T) {
	if os.Getenv("ATHANOR_RUN_BATCH") == "" {
		t.Skip("set ATHANOR_RUN_BATCH=1 to run full 22-book regression")
	}

	baselinePath := filepath.Join("testdata", "batch_regression_baseline.json")
	data, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}

	var baseline []batchBaselineRecord
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatalf("unmarshal baseline: %v", err)
	}

	for _, record := range baseline {
		record := record
		t.Run(record.FileName, func(t *testing.T) {
			outputDir := testOutputDir(t, "batch-"+sanitizePathComponent(record.FileName))
			input := filepath.Join("..", "..", "epub-raw", record.FileName)

			result, err := ConvertEPUB(context.Background(), input, Options{
				OutputRootDir: outputDir,
				BaseName:      "batch",
			})
			if err != nil {
				t.Fatalf("ConvertEPUB failed: %v", err)
			}

			if result.Stats != record.Stats {
				t.Fatalf("stats mismatch for %s: got %+v want %+v", record.FileName, result.Stats, record.Stats)
			}

			gotTitles := loadTitlesForTest(t, filepath.Join(result.ArtifactDir, "toc.json"))
			if !equalStrings(gotTitles, record.ChapterTitles) {
				t.Fatalf("chapter titles mismatch for %s", record.FileName)
			}

			gotFingerprints := loadChunkFingerprintsForTest(t, result.ChunksPath, max(3, len(record.ChunkFingerprints)))
			if !equalStrings(gotFingerprints, record.ChunkFingerprints) {
				t.Fatalf("chunk fingerprints mismatch for %s", record.FileName)
			}

			if len(record.ChunkCharacterStats) > 0 {
				gotStats := loadChunkCharacterStatsForTest(t, result.ChunksPath)
				if !equalInts(gotStats, record.ChunkCharacterStats) {
					t.Fatalf("chunk character stats mismatch for %s: got %v want %v", record.FileName, gotStats, record.ChunkCharacterStats)
				}
			}
		})
	}
}

func loadTitlesForTest(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read toc: %v", err)
	}
	var toc []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(data, &toc); err != nil {
		t.Fatalf("unmarshal toc: %v", err)
	}
	out := make([]string, 0, len(toc))
	for _, item := range toc {
		out = append(out, item.Title)
	}
	return out
}

func loadChunkFingerprintsForTest(t *testing.T, path string, limit int) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read chunks: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	selected := sampleFingerprintLines(lines, limit)
	out := make([]string, 0, len(selected))
	for _, line := range selected {
		var chunk struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatalf("unmarshal chunk: %v", err)
		}
		text := []rune(strings.TrimSpace(chunk.Text))
		if len(text) > 100 {
			text = text[:100]
		}
		out = append(out, string(text))
	}
	return out
}

func loadChunkCharacterStatsForTest(t *testing.T, path string) []int {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read chunks: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	values := make([]int, 0, len(lines))
	for _, line := range lines {
		var chunk struct {
			CharacterSize int `json:"characterSize"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatalf("unmarshal chunk size: %v", err)
		}
		values = append(values, chunk.CharacterSize)
	}
	return summarizeChunkDistribution(values)
}

func sampleFingerprintLines(lines []string, limit int) []string {
	if len(lines) <= limit || limit <= 0 {
		return append([]string(nil), lines...)
	}
	selected := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		index := i * (len(lines) - 1) / (limit - 1)
		selected = append(selected, lines[index])
	}
	return selected
}

func summarizeChunkDistribution(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]int(nil), values...)
	sortInts(sorted)
	return []int{
		sorted[0],
		percentileInts(sorted, 25),
		percentileInts(sorted, 50),
		percentileInts(sorted, 75),
		sorted[len(sorted)-1],
	}
}

func percentileInts(values []int, pct int) int {
	if len(values) == 0 {
		return 0
	}
	if pct <= 0 {
		return values[0]
	}
	if pct >= 100 {
		return values[len(values)-1]
	}
	index := (len(values) - 1) * pct / 100
	return values[index]
}

func sortInts(values []int) {
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
