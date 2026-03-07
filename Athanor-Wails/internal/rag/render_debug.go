package rag

import (
	"fmt"
	"strings"
)

func RenderDebugMarkdown(book Book) string {
	var parts []string
	parts = append(parts, "# "+safeTitle(book.Metadata.Title), "")
	parts = append(parts, "## Debug", "")
	parts = append(parts, fmt.Sprintf("- source_path: %s", book.Metadata.SourcePath))
	parts = append(parts, fmt.Sprintf("- source_sha256: %s", book.Metadata.SourceSHA256))
	parts = append(parts, fmt.Sprintf("- language: %s", book.Metadata.Language))
	parts = append(parts, fmt.Sprintf("- chapters: %d", book.Stats.ChapterCount))
	parts = append(parts, fmt.Sprintf("- backmatter: %d", book.Stats.BackMatterCount))
	parts = append(parts, fmt.Sprintf("- footnotes: %d", book.Stats.FootnoteCount), "")

	for _, chapter := range book.Main {
		parts = append(parts, renderDebugChapter(chapter, 2))
	}
	for _, chapter := range book.Back {
		parts = append(parts, renderDebugChapter(chapter, 2))
	}
	return strings.TrimSpace(strings.Join(parts, "\n")) + "\n"
}

func renderDebugChapter(chapter Chapter, topLevel int) string {
	var parts []string
	parts = append(parts, strings.Repeat("#", topLevel)+" "+displayChapterTitle(chapter), "")
	parts = append(parts, fmt.Sprintf("- chapter_id: %s", chapter.ID))
	parts = append(parts, fmt.Sprintf("- order: %d", chapter.Order))
	parts = append(parts, fmt.Sprintf("- kind: %s", chapter.Kind))
	parts = append(parts, fmt.Sprintf("- source_ref: %s", chapter.SourceRef))
	if chapter.ClassifyReason != "" {
		parts = append(parts, fmt.Sprintf("- classify_reason: %s", chapter.ClassifyReason))
	}
	if chapter.tocTrimmed > 0 {
		parts = append(parts, fmt.Sprintf("- toc_trimmed: %d", chapter.tocTrimmed))
	}
	if chapter.crossFileNotes > 0 {
		parts = append(parts, fmt.Sprintf("- cross_file_notes: %d", chapter.crossFileNotes))
	}
	for _, warning := range chapter.warnings {
		parts = append(parts, fmt.Sprintf("- warning: %s", warning))
	}
	parts = append(parts, "")
	parts = append(parts, renderBlocks(chapter.Blocks, topLevel+1))
	if len(chapter.Footnotes) > 0 {
		parts = append(parts, "", strings.Repeat("#", topLevel+1)+" Footnotes", "")
		for _, note := range chapter.Footnotes {
			parts = append(parts, fmt.Sprintf("[^%s]: %s", note.Label, note.Content))
		}
	}
	parts = append(parts, "")
	return strings.Join(parts, "\n")
}
