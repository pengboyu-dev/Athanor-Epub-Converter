package rag

import (
	"sort"
	"time"
)

const pipelineVersion = "v0.4"

func BuildDiagnostics(book Book, chunks []Chunk, config ChunkConfig) Diagnostics {
	config = normalizeChunkConfig(config)
	chunkCounts := make(map[string]int, len(book.Main)+len(book.Back))
	chunkCharsByChapter := make(map[string][]int, len(book.Main)+len(book.Back))
	chunkDiagnostics := make([]ChunkDiagnostic, 0, len(chunks))
	totalChunkChars := 0
	maxChunkChars := 0
	minChunkChars := 0
	shortChunkCount := 0
	oversizeChunkCount := 0
	allChunkChars := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		chunkCounts[chunk.ChapterID]++
		totalChunkChars += chunk.CharacterSize
		chunkCharsByChapter[chunk.ChapterID] = append(chunkCharsByChapter[chunk.ChapterID], chunk.CharacterSize)
		allChunkChars = append(allChunkChars, chunk.CharacterSize)
		chunkWarnings := make([]string, 0, 2)
		if minChunkChars == 0 || chunk.CharacterSize < minChunkChars {
			minChunkChars = chunk.CharacterSize
		}
		if chunk.CharacterSize > maxChunkChars {
			maxChunkChars = chunk.CharacterSize
		}
		if chunk.CharacterSize < config.MinSize {
			shortChunkCount++
			chunkWarnings = append(chunkWarnings, "chunk:short")
		}
		if chunk.CharacterSize > config.MaxSize {
			oversizeChunkCount++
			chunkWarnings = append(chunkWarnings, "chunk:oversize")
		}
		if chunk.BlockCount == 0 {
			chunkWarnings = append(chunkWarnings, "chunk:no_blocks")
		}
		chunkDiagnostics = append(chunkDiagnostics, ChunkDiagnostic{
			ID:            chunk.ID,
			ChapterID:     chunk.ChapterID,
			ChapterTitle:  chunk.ChapterTitle,
			ChapterOrder:  chunk.ChapterOrder,
			Kind:          chunk.Kind,
			Sequence:      chunk.Sequence,
			CharacterSize: chunk.CharacterSize,
			BlockCount:    chunk.BlockCount,
			HeadingPath:   append([]string(nil), chunk.HeadingPath...),
			HasFootnotes:  chunk.HasFootnotes,
			TokenEstimate: chunk.TokenEstimate,
			Warnings:      chunkWarnings,
		})
	}

	all := append(append([]Chapter(nil), book.Main...), book.Back...)
	chapters := make([]ChapterDiagnostic, 0, len(all))
	tocTrimmed := 0
	crossFileNotes := 0
	for _, chapter := range all {
		tocTrimmed += chapter.tocTrimmed
		crossFileNotes += chapter.crossFileNotes
		chunkChars := chunkCharsByChapter[chapter.ID]
		chapterWarnings := append([]string(nil), chapter.warnings...)
		shortChunks := 0
		oversizeChunks := 0
		chapterTotalChars := 0
		chapterMinChars := 0
		chapterMaxChars := 0
		for _, size := range chunkChars {
			chapterTotalChars += size
			if chapterMinChars == 0 || size < chapterMinChars {
				chapterMinChars = size
			}
			if size > chapterMaxChars {
				chapterMaxChars = size
			}
			if size < config.MinSize {
				shortChunks++
			}
			if size > config.MaxSize {
				oversizeChunks++
			}
		}
		if chapter.Kind == ChapterKindMain && len(chapter.Blocks) > 0 && len(chunkChars) == 0 {
			chapterWarnings = append(chapterWarnings, "chunk:no_output")
		}
		if shortChunks > 0 {
			chapterWarnings = append(chapterWarnings, "chunk:short_segments")
		}
		if oversizeChunks > 0 {
			chapterWarnings = append(chapterWarnings, "chunk:oversize_segments")
		}
		avgChapterChars := 0
		if len(chunkChars) > 0 {
			avgChapterChars = chapterTotalChars / len(chunkChars)
		}
		chapters = append(chapters, ChapterDiagnostic{
			ID:                       chapter.ID,
			Title:                    chapter.Title,
			Order:                    chapter.Order,
			Kind:                     chapter.Kind,
			ClassifyReason:           chapter.ClassifyReason,
			SourceRef:                chapter.SourceRef,
			BlockCount:               len(chapter.Blocks),
			FootnoteCount:            len(chapter.Footnotes),
			ChunkCount:               chunkCounts[chapter.ID],
			ShortChunkCount:          shortChunks,
			OversizeChunkCount:       oversizeChunks,
			MinChunkCharacters:       chapterMinChars,
			AverageChunkCharacters:   avgChapterChars,
			MaxChunkCharacters:       chapterMaxChars,
			TOCResidualBlocksRemoved: chapter.tocTrimmed,
			CrossFileFootnotesLinked: chapter.crossFileNotes,
			Warnings:                 chapterWarnings,
		})
	}

	avgChunkChars := 0
	if len(chunks) > 0 {
		avgChunkChars = totalChunkChars / len(chunks)
	}
	p50ChunkChars := percentile(allChunkChars, 50)
	p90ChunkChars := percentile(allChunkChars, 90)

	return Diagnostics{
		Summary: DiagnosticsSummary{
			PipelineVersion:          pipelineVersion,
			GeneratedAt:              time.Now().UTC().Format(time.RFC3339),
			SourcePath:               book.Metadata.SourcePath,
			SourceSHA256:             book.Metadata.SourceSHA256,
			Title:                    book.Metadata.Title,
			ChapterCount:             book.Stats.ChapterCount,
			FrontMatterCount:         book.Stats.FrontMatterCount,
			BackMatterCount:          book.Stats.BackMatterCount,
			ChunkCount:               len(chunks),
			FootnoteCount:            book.Stats.FootnoteCount,
			TOCResidualBlocksRemoved: tocTrimmed,
			CrossFileFootnotesLinked: crossFileNotes,
			ShortChunkCount:          shortChunkCount,
			OversizeChunkCount:       oversizeChunkCount,
			MinChunkCharacters:       minChunkChars,
			AverageChunkCharacters:   avgChunkChars,
			P50ChunkCharacters:       p50ChunkChars,
			P90ChunkCharacters:       p90ChunkChars,
			MaxChunkCharacters:       maxChunkChars,
		},
		Chapters: chapters,
		Chunks:   chunkDiagnostics,
	}
}

func percentile(values []int, pct int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	if pct <= 0 {
		return sorted[0]
	}
	if pct >= 100 {
		return sorted[len(sorted)-1]
	}
	index := (len(sorted) - 1) * pct / 100
	return sorted[index]
}
