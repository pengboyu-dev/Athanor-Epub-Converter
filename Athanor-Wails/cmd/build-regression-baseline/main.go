package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"Athanor-Wails/internal/rag"
)

type baselineRecord struct {
	FileName            string    `json:"fileName"`
	Stats               rag.Stats `json:"stats"`
	ChapterTitles       []string  `json:"chapterTitles"`
	ChunkFingerprints   []string  `json:"chunkFingerprints"`
	ChunkCharacterStats []int     `json:"chunkCharacterStats,omitempty"`
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fail(err)
	}

	sampleDir := filepath.Join(root, "epub-raw")
	files, err := filepath.Glob(filepath.Join(sampleDir, "*.epub"))
	if err != nil {
		fail(err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		fail(fmt.Errorf("no epub samples found in %s", sampleDir))
	}

	outDir := filepath.Join(root, "internal", "rag", "testdata")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fail(err)
	}

	records := make([]baselineRecord, 0, len(files))
	for _, file := range files {
		result, err := rag.ConvertEPUB(context.Background(), file, rag.Options{
			OutputRootDir: filepath.Join(root, ".tmp", "baseline"),
			BaseName:      trimExt(filepath.Base(file)),
		})
		if err != nil {
			fail(fmt.Errorf("%s: %w", filepath.Base(file), err))
		}
		records = append(records, baselineRecord{
			FileName:            filepath.Base(file),
			Stats:               result.Stats,
			ChapterTitles:       loadChapterTitles(filepath.Join(result.ArtifactDir, "toc.json")),
			ChunkFingerprints:   loadChunkFingerprints(result.ChunksPath, 10),
			ChunkCharacterStats: loadChunkCharacterStats(result.ChunksPath),
		})
		fmt.Printf("%s -> chapters=%d chunks=%d\n", filepath.Base(file), result.Stats.ChapterCount, result.Stats.ChunkCount)
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		fail(err)
	}

	output := filepath.Join(outDir, "batch_regression_baseline.json")
	if err := os.WriteFile(output, data, 0o644); err != nil {
		fail(err)
	}

	fmt.Printf("wrote %s (%d records)\n", output, len(records))
}

func trimExt(name string) string {
	return name[:len(name)-len(filepath.Ext(name))]
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func loadChapterTitles(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	var toc []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(data, &toc); err != nil {
		fail(err)
	}
	titles := make([]string, 0, len(toc))
	for _, item := range toc {
		titles = append(titles, item.Title)
	}
	return titles
}

func loadChunkFingerprints(path string, limit int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	selected := sampleLines(lines, limit)
	out := make([]string, 0, len(selected))
	for _, line := range selected {
		var chunk struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			fail(err)
		}
		text := []rune(strings.TrimSpace(chunk.Text))
		if len(text) > 100 {
			text = text[:100]
		}
		out = append(out, string(text))
	}
	return out
}

func loadChunkCharacterStats(path string) []int {
	data, err := os.ReadFile(path)
	if err != nil {
		fail(err)
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
			fail(err)
		}
		values = append(values, chunk.CharacterSize)
	}
	sort.Ints(values)
	return summarizeIntDistribution(values)
}

func sampleLines(lines []string, limit int) []string {
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

func summarizeIntDistribution(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	return []int{
		values[0],
		percentile(values, 25),
		percentile(values, 50),
		percentile(values, 75),
		values[len(values)-1],
	}
}

func percentile(values []int, pct int) int {
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
