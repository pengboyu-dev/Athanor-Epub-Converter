package rag

import (
	"strings"
	"testing"
)

func TestRenderTableSeparatorRow(t *testing.T) {
	rows := [][]string{
		{"A", "B", "C"},
		{"1", "2", "3"},
	}

	lines := renderTable(rows)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[1] != "| --- | --- | --- |" {
		t.Fatalf("unexpected separator row: %q", lines[1])
	}
}

func TestRenderChapterMarkdownIncludesFootnotes(t *testing.T) {
	book := Book{
		Main: []Chapter{
			{
				ID:        "chapter-001",
				Title:     "One",
				Order:     1,
				Kind:      ChapterKindMain,
				SourceRef: "one.xhtml",
				Blocks:    []Block{{Kind: BlockKindParagraph, Text: "Hello"}},
				Footnotes: []Footnote{{Label: "1", Content: "Note body"}},
			},
		},
	}

	out := RenderChapterMarkdown(book)["chapter-001"]
	if !strings.Contains(out, "## 脚注") {
		t.Fatalf("expected footnote section, got %q", out)
	}
	if !strings.Contains(out, "[^1]: Note body") {
		t.Fatalf("expected rendered footnote, got %q", out)
	}
}