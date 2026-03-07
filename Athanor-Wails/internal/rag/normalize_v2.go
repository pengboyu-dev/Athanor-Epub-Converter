package rag

import "strings"

func NormalizeBook(book *Book) {
	book.Main = normalizeChapterListV2(book.Main)
	book.Back = normalizeChapterListV2(book.Back)
	recomputeStats(book)
}

func normalizeChapterListV2(chapters []Chapter) []Chapter {
	out := make([]Chapter, 0, len(chapters))
	for _, chapter := range chapters {
		chapter.tocTrimmed = trimTOCResidualBlocks(&chapter)
		chapter.Blocks = normalizeBlocksV2(chapter.Blocks)
		chapter.Footnotes = normalizeFootnotesV2(chapter.Footnotes)
		chapter.Title = normalizeParagraphV2(chapter.Title)
		if len(chapter.Blocks) == 0 && len(chapter.Footnotes) == 0 {
			continue
		}
		if chapter.Title == "" {
			chapter.Title = "Untitled Section"
		}
		out = append(out, chapter)
	}
	return out
}

func normalizeBlocksV2(blocks []Block) []Block {
	out := make([]Block, 0, len(blocks))
	for _, block := range blocks {
		switch block.Kind {
		case BlockKindParagraph, BlockKindBlockquote, BlockKindHeading:
			block.Text = normalizeParagraphV2(block.Text)
			if block.Text == "" {
				continue
			}
		case BlockKindCode:
			block.Text = strings.TrimSpace(block.Text)
			if block.Text == "" {
				continue
			}
		case BlockKindList:
			items := make([]string, 0, len(block.Items))
			for _, item := range block.Items {
				item = normalizeParagraphV2(item)
				if item != "" {
					items = append(items, item)
				}
			}
			if len(items) == 0 {
				continue
			}
			block.Items = items
		case BlockKindTable:
			rows := make([][]string, 0, len(block.Rows))
			for _, row := range block.Rows {
				cells := make([]string, 0, len(row))
				for _, cell := range row {
					cells = append(cells, normalizeParagraphV2(cell))
				}
				rows = append(rows, cells)
			}
			if len(rows) == 0 {
				continue
			}
			block.Rows = rows
		}

		if len(out) > 0 && duplicateBlockV2(out[len(out)-1], block) {
			continue
		}
		out = append(out, block)
	}
	return out
}

func normalizeFootnotesV2(notes []Footnote) []Footnote {
	out := make([]Footnote, 0, len(notes))
	for _, note := range notes {
		note.Content = normalizeParagraphV2(note.Content)
		if note.Content == "" {
			continue
		}
		if strings.TrimSpace(note.Label) == "" {
			note.Label = note.ID
		}
		out = append(out, note)
	}
	return out
}

func normalizeParagraphV2(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	text = strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff':
			return -1
		case '\u00a0', '\u3000', '\t', '\r', '\n':
			return ' '
		default:
			return r
		}
	}, text)

	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}

	runes := []rune(text)
	out := make([]rune, 0, len(runes))
	for i, r := range runes {
		if r != ' ' {
			out = append(out, r)
			continue
		}
		if i == 0 || i == len(runes)-1 {
			continue
		}
		if shouldDropSpaceV2(runes[i-1], runes[i+1]) {
			continue
		}
		out = append(out, r)
	}

	return strings.TrimSpace(string(out))
}

func shouldDropSpaceV2(prev, next rune) bool {
	switch {
	case isCJKRune(prev) && isCJKRune(next):
		return true
	case isCJKRune(prev) && isCJKPunctuation(next):
		return true
	case isCJKPunctuation(prev) && isCJKRune(next):
		return true
	case isCJKPunctuation(prev) && isCJKPunctuation(next):
		return true
	default:
		return false
	}
}

func duplicateBlockV2(prev, current Block) bool {
	if prev.Kind != current.Kind {
		return false
	}
	switch prev.Kind {
	case BlockKindParagraph, BlockKindBlockquote, BlockKindHeading, BlockKindCode:
		return prev.Text == current.Text
	default:
		return false
	}
}

func recomputeStats(book *Book) {
	if book == nil {
		return
	}

	book.Stats.ChapterCount = len(book.Main)
	book.Stats.FrontMatterCount = 0
	book.Stats.BackMatterCount = 0
	book.Stats.FootnoteCount = 0

	for _, chapter := range book.Main {
		book.Stats.FootnoteCount += len(chapter.Footnotes)
	}
	for _, chapter := range book.Back {
		if chapter.Kind == ChapterKindFrontMatter {
			book.Stats.FrontMatterCount++
		} else {
			book.Stats.BackMatterCount++
		}
		book.Stats.FootnoteCount += len(chapter.Footnotes)
	}
}

func trimTOCResidualBlocks(chapter *Chapter) int {
	if chapter == nil || len(chapter.Blocks) == 0 {
		return 0
	}

	if chapterLooksLikeTOC(*chapter) {
		kept := make([]Block, 0, len(chapter.Blocks))
		removed := 0
		for _, block := range chapter.Blocks {
			if blockLooksLikeTOCResidual(block) {
				removed++
				continue
			}
			kept = append(kept, block)
		}
		chapter.Blocks = kept
		return removed
	}

	trimUntil := 0
	for i, block := range chapter.Blocks {
		if blockLooksLikeTOCResidual(block) {
			trimUntil = i + 1
			continue
		}
		break
	}
	if trimUntil == 0 {
		return 0
	}

	chapter.Blocks = append([]Block(nil), chapter.Blocks[trimUntil:]...)
	return trimUntil
}

func chapterLooksLikeTOC(chapter Chapter) bool {
	title := normalizeTitle(chapter.Title)
	reason := strings.ToLower(chapter.ClassifyReason)
	if title == "toc" || title == "contents" || title == "table of contents" || title == "目录" || title == "目次" {
		return true
	}
	return strings.Contains(reason, "toc") || strings.Contains(reason, "contents")
}

func blockLooksLikeTOCResidual(block Block) bool {
	switch block.Kind {
	case BlockKindHeading, BlockKindParagraph, BlockKindBlockquote:
		return textLooksLikeTOCResidual(block.Text)
	case BlockKindList:
		if len(block.Items) == 0 {
			return false
		}
		matched := 0
		for _, item := range block.Items {
			if textLooksLikeTOCResidual(item) {
				matched++
			}
		}
		return matched >= len(block.Items)/2+1
	default:
		return false
	}
}

func textLooksLikeTOCResidual(text string) bool {
	text = normalizeParagraphV2(text)
	lower := strings.ToLower(text)
	if lower == "" {
		return false
	}
	switch lower {
	case "toc", "contents", "table of contents", "目录", "目次":
		return true
	}
	if strings.Contains(text, "....") || strings.Contains(text, "．．") || strings.Contains(text, "· ·") {
		if hasTrailingPageMarker(text) {
			return true
		}
	}
	if hasTrailingPageMarker(text) && looksLikeSectionLabel(lower) {
		return true
	}
	return false
}

func looksLikeSectionLabel(text string) bool {
	switch {
	case strings.HasPrefix(text, "chapter "):
		return true
	case strings.HasPrefix(text, "part "):
		return true
	case strings.HasPrefix(text, "book "):
		return true
	case strings.HasPrefix(text, "section "):
		return true
	case strings.HasPrefix(text, "第") && strings.Contains(text, "章"):
		return true
	case strings.HasPrefix(text, "第") && strings.Contains(text, "节"):
		return true
	default:
		return false
	}
}

func hasTrailingPageMarker(text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	last := strings.Trim(fields[len(fields)-1], ".·•-—_()[]")
	if last == "" {
		return false
	}
	if isASCIIPageNumber(last) {
		return true
	}
	switch last {
	case "i", "ii", "iii", "iv", "v", "vi", "vii", "viii", "ix", "x", "xi", "xii":
		return true
	default:
		return false
	}
}

func isASCIIPageNumber(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
