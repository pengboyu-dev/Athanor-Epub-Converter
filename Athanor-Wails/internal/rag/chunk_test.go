package rag

import (
	"strings"
	"testing"
)

func TestBuildChunksUsesPerChapterIDs(t *testing.T) {
	book := Book{
		Metadata: Metadata{Title: "Book"},
		Main: []Chapter{
			{
				ID:        "chapter-001",
				Title:     "One",
				Order:     1,
				Kind:      ChapterKindMain,
				SourceRef: "one.xhtml",
				Blocks:    []Block{{Kind: BlockKindParagraph, Text: strings.Repeat("A", 900)}, {Kind: BlockKindParagraph, Text: strings.Repeat("B", 900)}},
			},
			{
				ID:        "chapter-002",
				Title:     "Two",
				Order:     2,
				Kind:      ChapterKindMain,
				SourceRef: "two.xhtml",
				Blocks:    []Block{{Kind: BlockKindParagraph, Text: strings.Repeat("C", 900)}},
			},
		},
	}

	chunks := BuildChunks(book, ChunkConfig{})
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	if chunks[0].ID != "chapter-001-001" || chunks[1].ID != "chapter-001-002" {
		t.Fatalf("unexpected chapter-001 chunk ids: %+v", chunks[:2])
	}
	if chunks[2].ID != "chapter-002-001" {
		t.Fatalf("expected chapter-002 to restart numbering, got %s", chunks[2].ID)
	}
	if chunks[2].Sequence != 3 {
		t.Fatalf("expected global sequence 3, got %d", chunks[2].Sequence)
	}
}

func TestBuildChunksPreservesHeadingContext(t *testing.T) {
	book := Book{
		Metadata: Metadata{Title: "Book"},
		Main: []Chapter{
			{
				ID:        "chapter-001",
				Title:     "One",
				Order:     1,
				Kind:      ChapterKindMain,
				SourceRef: "one.xhtml",
				Blocks: []Block{
					{Kind: BlockKindHeading, Text: "Section A", Level: 2},
					{Kind: BlockKindParagraph, Text: strings.Repeat("A", 900)},
					{Kind: BlockKindParagraph, Text: strings.Repeat("B", 900)},
				},
			},
		},
	}

	chunks := BuildChunks(book, ChunkConfig{})
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "## Section A") {
		t.Fatalf("expected heading context in first chunk: %q", chunks[0].Text)
	}
	if !strings.Contains(chunks[1].Text, "## Section A") {
		t.Fatalf("expected heading context in second chunk: %q", chunks[1].Text)
	}
}

func TestBuildChunksPreservesHeadingLevelsAndBlockCount(t *testing.T) {
	book := Book{
		Metadata: Metadata{Title: "Book"},
		Main: []Chapter{
			{
				ID:        "chapter-001",
				Title:     "One",
				Order:     1,
				Kind:      ChapterKindMain,
				SourceRef: "one.xhtml",
				Blocks: []Block{
					{Kind: BlockKindHeading, Text: "Part", Level: 2},
					{Kind: BlockKindHeading, Text: "Detail", Level: 3},
					{Kind: BlockKindParagraph, Text: strings.Repeat("A. ", 800)},
				},
			},
		},
	}

	chunks := BuildChunks(book, ChunkConfig{})
	if len(chunks) < 2 {
		t.Fatalf("expected split chunks, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "## Part\n### Detail") {
		t.Fatalf("expected hierarchical heading path, got %q", chunks[0].Text)
	}
	if len(chunks[0].HeadingPath) != 2 || chunks[0].HeadingPath[0] != "## Part" || chunks[0].HeadingPath[1] != "### Detail" {
		t.Fatalf("expected structured heading path, got %+v", chunks[0].HeadingPath)
	}
	if chunks[0].TokenEstimate == 0 {
		t.Fatalf("expected token estimate, got %+v", chunks[0])
	}
	totalBlocks := 0
	for _, chunk := range chunks {
		totalBlocks += chunk.BlockCount
	}
	if totalBlocks != 1 {
		t.Fatalf("expected split paragraph to count as one source block, got %d", totalBlocks)
	}
}

func TestChunkTextUsesSharedTableRendering(t *testing.T) {
	block := Block{
		Kind: BlockKindTable,
		Rows: [][]string{
			{"A", "B"},
			{"1", "2"},
		},
	}

	text := chunkText(block)
	if !strings.Contains(text, "| --- | --- |") {
		t.Fatalf("expected markdown table separator in chunk text, got %q", text)
	}
}

func TestBuildChunksAttachesReferencedFootnotes(t *testing.T) {
	book := Book{
		Metadata: Metadata{Title: "Book", Language: "en"},
		Main: []Chapter{
			{
				ID:        "chapter-001",
				Title:     "One",
				Order:     1,
				Kind:      ChapterKindMain,
				SourceRef: "one.xhtml",
				Blocks: []Block{
					{Kind: BlockKindParagraph, Text: strings.Repeat("Body with note[^1]. ", 40)},
				},
				Footnotes: []Footnote{
					{Label: "1", Content: "Attached note"},
					{Label: "2", Content: "Orphan note"},
				},
			},
		},
	}

	chunks := BuildChunks(book, ChunkConfig{TargetSize: 600, MinSize: 200, MaxSize: 900})
	if len(chunks) < 2 {
		t.Fatalf("expected body chunk plus orphan footnote chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "## Footnotes\n[^1]: Attached note") {
		t.Fatalf("expected referenced footnote attached to body chunk, got %q", chunks[0].Text)
	}
	if !chunks[0].HasFootnotes {
		t.Fatalf("expected body chunk to mark footnotes attached: %+v", chunks[0])
	}
	last := chunks[len(chunks)-1]
	if !strings.Contains(last.Text, "[^2]: Orphan note") {
		t.Fatalf("expected orphan footnote chunk, got %q", last.Text)
	}
}

func TestBuildChunksCanIncludeBackmatter(t *testing.T) {
	book := Book{
		Metadata: Metadata{Title: "Book"},
		Main: []Chapter{
			{
				ID:        "chapter-001",
				Title:     "Main",
				Order:     1,
				Kind:      ChapterKindMain,
				SourceRef: "main.xhtml",
				Blocks:    []Block{{Kind: BlockKindParagraph, Text: strings.Repeat("Main body. ", 80)}},
			},
		},
		Back: []Chapter{
			{
				ID:        "chapter-002",
				Title:     "Appendix",
				Order:     2,
				Kind:      ChapterKindBackMatter,
				SourceRef: "appendix.xhtml",
				Blocks:    []Block{{Kind: BlockKindParagraph, Text: strings.Repeat("Appendix body. ", 80)}},
			},
		},
	}

	chunks := BuildChunks(book, ChunkConfig{IncludeBackmatter: true})
	if len(chunks) < 2 {
		t.Fatalf("expected chunks for main and backmatter, got %d", len(chunks))
	}
	foundBackmatter := false
	for _, chunk := range chunks {
		if chunk.Kind == ChapterKindBackMatter {
			foundBackmatter = true
			break
		}
	}
	if !foundBackmatter {
		t.Fatalf("expected backmatter chunk when IncludeBackmatter is enabled: %+v", chunks)
	}
}
