package rag

import "testing"

func TestBuildDiagnosticsCapturesChunkWarnings(t *testing.T) {
	book := Book{
		Metadata: Metadata{
			Title:        "Book",
			SourcePath:   "sample.epub",
			SourceSHA256: "abc",
		},
		Stats: Stats{
			ChapterCount: 1,
			ChunkCount:   2,
		},
		Main: []Chapter{
			{
				ID:        "chapter-001",
				Title:     "One",
				Order:     1,
				Kind:      ChapterKindMain,
				SourceRef: "one.xhtml",
				Blocks:    []Block{{Kind: BlockKindParagraph, Text: "Body"}},
			},
		},
	}

	chunks := []Chunk{
		{ChapterID: "chapter-001", CharacterSize: 120, BlockCount: 1},
		{ChapterID: "chapter-001", CharacterSize: 1800, BlockCount: 1},
	}

	diagnostics := BuildDiagnostics(book, chunks, ChunkConfig{})
	if diagnostics.Summary.ShortChunkCount != 1 {
		t.Fatalf("expected 1 short chunk, got %d", diagnostics.Summary.ShortChunkCount)
	}
	if diagnostics.Summary.OversizeChunkCount != 1 {
		t.Fatalf("expected 1 oversize chunk, got %d", diagnostics.Summary.OversizeChunkCount)
	}
	if diagnostics.Summary.P50ChunkCharacters == 0 || diagnostics.Summary.P90ChunkCharacters == 0 {
		t.Fatalf("expected percentile stats, got %+v", diagnostics.Summary)
	}
	if len(diagnostics.Chapters) != 1 {
		t.Fatalf("expected one chapter diagnostic, got %d", len(diagnostics.Chapters))
	}
	if len(diagnostics.Chunks) != 2 {
		t.Fatalf("expected chunk diagnostics, got %d", len(diagnostics.Chunks))
	}
	if len(diagnostics.Chunks[0].Warnings) == 0 || len(diagnostics.Chunks[1].Warnings) == 0 {
		t.Fatalf("expected chunk warnings, got %+v", diagnostics.Chunks)
	}
	chapter := diagnostics.Chapters[0]
	if chapter.ShortChunkCount != 1 || chapter.OversizeChunkCount != 1 {
		t.Fatalf("expected chapter chunk warnings, got %+v", chapter)
	}
	if len(chapter.Warnings) < 2 {
		t.Fatalf("expected chapter warnings, got %+v", chapter.Warnings)
	}
}
