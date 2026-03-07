package rag

import (
	"fmt"
	"strings"
)

type blockRenderOptions struct {
	headingBase      int
	includeSeparator bool
}

func RenderBookMarkdown(book Book) string {
	var parts []string
	parts = append(parts, "# "+safeTitle(book.Metadata.Title), "")

	for _, chapter := range book.Main {
		parts = append(parts, renderChapter(chapter, 2, false))
	}
	for _, chapter := range book.Back {
		parts = append(parts, renderChapter(chapter, 2, true))
	}
	return strings.TrimSpace(strings.Join(parts, "\n")) + "\n"
}

func RenderChapterMarkdown(book Book) map[string]string {
	out := map[string]string{}
	all := append(append([]Chapter(nil), book.Main...), book.Back...)
	for _, chapter := range all {
		var parts []string
		parts = append(parts, "# "+displayChapterTitle(chapter), "")
		parts = append(parts, renderBlocks(chapter.Blocks, 2))
		if len(chapter.Footnotes) > 0 {
			parts = append(parts, "", "## 脚注", "")
			for _, note := range chapter.Footnotes {
				parts = append(parts, fmt.Sprintf("[^%s]: %s", note.Label, note.Content))
			}
		}
		out[chapter.ID] = strings.TrimSpace(strings.Join(parts, "\n")) + "\n"
	}
	return out
}

func renderChapter(chapter Chapter, topLevel int, forceTitle bool) string {
	var parts []string
	title := displayChapterTitle(chapter)
	if forceTitle || !sameMeaningfulTitle(chapter, title) {
		parts = append(parts, strings.Repeat("#", topLevel)+" "+title, "")
	}
	parts = append(parts, renderBlocks(chapter.Blocks, topLevel+1))
	if len(chapter.Footnotes) > 0 {
		parts = append(parts, "", strings.Repeat("#", topLevel+1)+" 脚注", "")
		for _, note := range chapter.Footnotes {
			parts = append(parts, fmt.Sprintf("[^%s]: %s", note.Label, note.Content))
		}
	}
	parts = append(parts, "")
	return strings.Join(parts, "\n")
}

func renderBlocks(blocks []Block, headingBase int) string {
	var parts []string
	for _, block := range blocks {
		lines := renderBlockLines(block, blockRenderOptions{
			headingBase:      headingBase,
			includeSeparator: true,
		})
		if len(lines) == 0 {
			continue
		}
		parts = append(parts, lines...)
		parts = append(parts, "")
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func renderBlockLines(block Block, opts blockRenderOptions) []string {
	switch block.Kind {
	case BlockKindHeading:
		level := opts.headingBase + block.Level - 1
		if level < opts.headingBase {
			level = opts.headingBase
		}
		return []string{strings.Repeat("#", level) + " " + block.Text}
	case BlockKindParagraph:
		return []string{block.Text}
	case BlockKindBlockquote:
		return []string{"> " + block.Text}
	case BlockKindList:
		lines := make([]string, 0, len(block.Items))
		for index, item := range block.Items {
			prefix := "- "
			if block.Ordered {
				prefix = fmt.Sprintf("%d. ", index+1)
			}
			lines = append(lines, prefix+item)
		}
		return lines
	case BlockKindCode:
		return []string{"```", block.Text, "```"}
	case BlockKindTable:
		return renderTable(block.Rows)
	case BlockKindSeparator:
		if opts.includeSeparator {
			return []string{"---"}
		}
		return nil
	default:
		return nil
	}
}

func renderTable(rows [][]string) []string {
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil
	}
	header := rows[0]
	lines := []string{
		"| " + strings.Join(header, " | ") + " |",
		"| " + strings.TrimRight(strings.Repeat("--- | ", len(header)), " "),
	}
	for _, row := range rows[1:] {
		cells := append([]string(nil), row...)
		for len(cells) < len(header) {
			cells = append(cells, "")
		}
		lines = append(lines, "| "+strings.Join(cells[:len(header)], " | ")+" |")
	}
	return lines
}

func safeTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "未命名图书"
	}
	return title
}

func displayChapterTitle(chapter Chapter) string {
	title := strings.TrimSpace(chapter.Title)
	if title == "" {
		return chapter.ID
	}
	return title
}

func sameMeaningfulTitle(chapter Chapter, title string) bool {
	for _, block := range chapter.Blocks {
		if block.Kind != BlockKindHeading {
			continue
		}
		return normalizeInlineText(block.Text) == normalizeInlineText(title)
	}
	return false
}
