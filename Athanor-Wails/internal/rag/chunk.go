package rag

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	targetChunkSize = 1200
	minChunkSize    = 500
	maxChunkSize    = 1700
)

type chunkUnit struct {
	text       string
	blockCount int
	isHeading  bool
	level      int
}

var footnoteRefRe = regexp.MustCompile(`\[\^([^\]]+)\]`)

func BuildChunks(book Book, config ChunkConfig) []Chunk {
	config = normalizeChunkConfig(config)

	var chunks []Chunk
	globalSequence := 0
	chapters := append([]Chapter(nil), book.Main...)
	if config.IncludeBackmatter {
		chapters = append(chapters, book.Back...)
	}

	for _, chapter := range chapters {
		chapterSequence := 0
		units, noteIndex := buildChunkUnits(chapter, config)
		usedNotes := map[string]struct{}{}
		var bucket []string
		bucketSize := 0
		bucketBlocks := 0
		var pendingHeadings []string

		flush := func() {
			text := strings.TrimSpace(strings.Join(bucket, "\n\n"))
			if text == "" {
				bucket = nil
				bucketSize = 0
				bucketBlocks = 0
				return
			}
			attachedNotes := footnotesForChunk(text, noteIndex, usedNotes)
			if len(attachedNotes) > 0 {
				text += "\n\n## Footnotes\n" + strings.Join(attachedNotes, "\n")
			}
			globalSequence++
			chapterSequence++
			headingPath := append([]string(nil), pendingHeadings...)
			chunks = append(chunks, Chunk{
				ID:            fmt.Sprintf("%s-%03d", chapter.ID, chapterSequence),
				BookTitle:     safeTitle(book.Metadata.Title),
				ChapterID:     chapter.ID,
				ChapterTitle:  chapter.Title,
				ChapterOrder:  chapter.Order,
				Kind:          chapter.Kind,
				Sequence:      globalSequence,
				Text:          text,
				SourcePath:    book.Metadata.SourcePath,
				SourceRef:     chapter.SourceRef,
				CharacterSize: len([]rune(text)),
				BlockCount:    bucketBlocks,
				HeadingPath:   headingPath,
				Language:      book.Metadata.Language,
				HasFootnotes:  len(attachedNotes) > 0,
				TokenEstimate: estimateTokens(text, book.Metadata.Language),
			})
			bucket = nil
			bucketSize = 0
			bucketBlocks = 0
		}

		for _, unit := range units {
			if unit.isHeading {
				if bucketSize >= config.MinSize {
					flush()
				}
				pendingHeadings = appendHeadingPath(pendingHeadings, unit.text, unit.level)
				continue
			}

			if len(bucket) == 0 && len(pendingHeadings) > 0 {
				bucket = append(bucket, strings.Join(pendingHeadings, "\n"))
				bucketSize = len([]rune(strings.Join(bucket, "\n\n")))
			}

			nextSize := bucketSize
			if nextSize > 0 {
				nextSize += 2
			}
			nextSize += len([]rune(unit.text))
			if nextSize > config.MaxSize && bucketSize >= config.MinSize {
				flush()
				if len(pendingHeadings) > 0 {
					bucket = append(bucket, strings.Join(pendingHeadings, "\n"))
					bucketSize = len([]rune(strings.Join(bucket, "\n\n")))
				}
			}

			if len(bucket) == 0 && len(pendingHeadings) > 0 {
				bucket = append(bucket, strings.Join(pendingHeadings, "\n"))
				bucketSize = len([]rune(strings.Join(bucket, "\n\n")))
			}

			bucket = append(bucket, unit.text)
			if bucketSize == 0 {
				bucketSize = len([]rune(unit.text))
			} else {
				bucketSize = len([]rune(strings.Join(bucket, "\n\n")))
			}
			bucketBlocks += unit.blockCount

			if bucketSize >= config.TargetSize {
				flush()
			}
		}
		if orphanNotes := orphanFootnotes(chapter.Footnotes, usedNotes); len(orphanNotes) > 0 {
			if len(bucket) > 0 {
				flush()
			}
			globalSequence++
			chapterSequence++
			text := "## Footnotes\n" + strings.Join(orphanNotes, "\n")
			chunks = append(chunks, Chunk{
				ID:            fmt.Sprintf("%s-%03d", chapter.ID, chapterSequence),
				BookTitle:     safeTitle(book.Metadata.Title),
				ChapterID:     chapter.ID,
				ChapterTitle:  chapter.Title,
				ChapterOrder:  chapter.Order,
				Kind:          chapter.Kind,
				Sequence:      globalSequence,
				Text:          text,
				SourcePath:    book.Metadata.SourcePath,
				SourceRef:     chapter.SourceRef,
				CharacterSize: len([]rune(text)),
				BlockCount:    len(orphanNotes),
				HeadingPath:   []string{"## Footnotes"},
				Language:      book.Metadata.Language,
				HasFootnotes:  true,
				TokenEstimate: estimateTokens(text, book.Metadata.Language),
			})
		}
		flush()
	}
	return chunks
}

func buildChunkUnits(chapter Chapter, config ChunkConfig) ([]chunkUnit, map[string]string) {
	units := make([]chunkUnit, 0, len(chapter.Blocks))
	noteIndex := make(map[string]string, len(chapter.Footnotes))
	for _, note := range chapter.Footnotes {
		label := strings.TrimSpace(note.Label)
		content := strings.TrimSpace(note.Content)
		if label != "" && content != "" {
			noteIndex[label] = content
		}
	}
	for _, block := range chapter.Blocks {
		switch block.Kind {
		case BlockKindHeading:
			text := strings.TrimSpace(block.Text)
			if text != "" {
				level := block.Level
				if level < 1 {
					level = 1
				}
				units = append(units, chunkUnit{
					text:       strings.Repeat("#", level) + " " + text,
					blockCount: 1,
					isHeading:  true,
					level:      level,
				})
			}
		default:
			text := chunkText(block)
			if text == "" {
				continue
			}
			for index, piece := range splitChunkText(text, config) {
				blockCount := 0
				if index == 0 {
					blockCount = 1
				}
				units = append(units, chunkUnit{text: piece, blockCount: blockCount})
			}
		}
	}
	return units, noteIndex
}

func appendHeadingPath(existing []string, heading string, level int) []string {
	if level < 1 {
		level = 1
	}
	if len(existing) == 0 {
		return []string{heading}
	}
	last := existing[len(existing)-1]
	if last == heading {
		return existing
	}
	if len(existing) >= level {
		existing = append([]string(nil), existing[:level-1]...)
	} else {
		existing = append([]string(nil), existing...)
	}
	return append(existing, heading)
}

func splitChunkText(text string, config ChunkConfig) []string {
	text = strings.TrimSpace(text)
	if len([]rune(text)) <= config.MaxSize {
		return []string{text}
	}

	segments := splitBySentence(text)
	if len(segments) <= 1 {
		return []string{text}
	}

	out := make([]string, 0, len(segments))
	var bucket []string
	bucketSize := 0
	flush := func() {
		if len(bucket) == 0 {
			return
		}
		out = append(out, strings.TrimSpace(strings.Join(bucket, " ")))
		bucket = nil
		bucketSize = 0
	}

	for _, segment := range segments {
		size := len([]rune(segment))
		if bucketSize > 0 && bucketSize+1+size > config.TargetSize {
			flush()
		}
		bucket = append(bucket, segment)
		if bucketSize == 0 {
			bucketSize = size
		} else {
			bucketSize += 1 + size
		}
	}
	flush()
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

func splitBySentence(text string) []string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return nil
	}

	var out []string
	start := 0
	for i, r := range runes {
		if !isSentenceBoundary(r) {
			continue
		}
		segment := strings.TrimSpace(string(runes[start : i+1]))
		if segment != "" {
			out = append(out, segment)
		}
		start = i + 1
	}
	if start < len(runes) {
		tail := strings.TrimSpace(string(runes[start:]))
		if tail != "" {
			out = append(out, tail)
		}
	}
	return out
}

func isSentenceBoundary(r rune) bool {
	switch r {
	case '.', '!', '?', ';', '\n', '\u3002', '\uff01', '\uff1f', '\uff1b':
		return true
	default:
		return false
	}
}

func chunkText(block Block) string {
	lines := renderBlockLines(block, blockRenderOptions{
		headingBase:      1,
		includeSeparator: false,
	})
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func normalizeChunkConfig(config ChunkConfig) ChunkConfig {
	if config.TargetSize <= 0 {
		config.TargetSize = targetChunkSize
	}
	if config.MinSize <= 0 {
		config.MinSize = minChunkSize
	}
	if config.MaxSize <= 0 {
		config.MaxSize = maxChunkSize
	}
	if config.MinSize > config.TargetSize {
		config.MinSize = config.TargetSize
	}
	if config.MaxSize < config.TargetSize {
		config.MaxSize = config.TargetSize
	}
	return config
}

func footnotesForChunk(text string, noteIndex map[string]string, usedNotes map[string]struct{}) []string {
	if len(noteIndex) == 0 {
		return nil
	}
	matches := footnoteRefRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		label := strings.TrimSpace(match[1])
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		content, ok := noteIndex[label]
		if !ok {
			continue
		}
		seen[label] = struct{}{}
		usedNotes[label] = struct{}{}
		out = append(out, fmt.Sprintf("[^%s]: %s", label, content))
	}
	return out
}

func orphanFootnotes(notes []Footnote, usedNotes map[string]struct{}) []string {
	if len(notes) == 0 {
		return nil
	}
	out := make([]string, 0, len(notes))
	for _, note := range notes {
		label := strings.TrimSpace(note.Label)
		content := strings.TrimSpace(note.Content)
		if label == "" || content == "" {
			continue
		}
		if _, ok := usedNotes[label]; ok {
			continue
		}
		out = append(out, fmt.Sprintf("[^%s]: %s", label, content))
	}
	return out
}

func estimateTokens(text, language string) int {
	chars := len([]rune(strings.TrimSpace(text)))
	if chars == 0 {
		return 0
	}
	language = strings.ToLower(strings.TrimSpace(language))
	if strings.HasPrefix(language, "zh") || strings.HasPrefix(language, "ja") || strings.HasPrefix(language, "ko") {
		return chars
	}
	return (chars + 3) / 4
}
