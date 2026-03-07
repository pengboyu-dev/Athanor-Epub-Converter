package rag

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

var whitespaceRe = regexp.MustCompile(`\s+`)

type chapterBuilder struct {
	chapter     Chapter
	title       string
	footnotes   []Footnote
	footnoteMap map[string]int
	noteTargets map[string]struct{}
	noteLookup  noteRegistry
}

func newChapterBuilder(sourceRef string, order int, tocTitle string, noteTargets map[string]struct{}, noteLookup noteRegistry) *chapterBuilder {
	return &chapterBuilder{
		chapter: Chapter{
			ID:        fmt.Sprintf("chapter-%03d", order),
			Title:     strings.TrimSpace(tocTitle),
			Order:     order,
			Kind:      ChapterKindMain,
			SourceRef: sourceRef,
			Blocks:    []Block{},
		},
		footnoteMap: map[string]int{},
		noteTargets: noteTargets,
		noteLookup:  noteLookup,
	}
}

func (b *chapterBuilder) build() Chapter {
	if b.chapter.Title == "" {
		if b.title != "" {
			b.chapter.Title = b.title
		} else {
			b.chapter.Title = fmt.Sprintf("章节 %03d", b.chapter.Order)
		}
	}
	b.chapter.Footnotes = b.footnotes
	return b.chapter
}

func (b *chapterBuilder) consumeChildren(parent *html.Node) {
	for child := parent.FirstChild; child != nil; child = child.NextSibling {
		b.consumeNode(child)
	}
}

func (b *chapterBuilder) consumeNode(node *html.Node) {
	if node.Type == html.TextNode {
		text := normalizeInlineText(node.Data)
		if text != "" {
			b.appendParagraph(text)
		}
		return
	}
	if node.Type != html.ElementNode {
		return
	}

	if isNoteNode(node) {
		b.captureFootnoteNode(node)
		return
	}

	switch node.Data {
	case "script", "style", "img", "svg", "figure", "video", "audio":
		return
	case "h1", "h2", "h3", "h4", "h5", "h6":
		text := strings.TrimSpace(b.inlineText(node))
		if text == "" {
			return
		}
		level := int(node.Data[1] - '0')
		if b.chapter.Title == "" {
			b.chapter.Title = text
		}
		if b.title == "" {
			b.title = text
		}
		b.chapter.Blocks = append(b.chapter.Blocks, Block{Kind: BlockKindHeading, Text: text, Level: level})
	case "p":
		b.appendParagraph(strings.TrimSpace(b.inlineText(node)))
	case "blockquote":
		text := strings.TrimSpace(b.inlineText(node))
		if text != "" {
			b.chapter.Blocks = append(b.chapter.Blocks, Block{Kind: BlockKindBlockquote, Text: text})
		}
	case "pre":
		code := strings.TrimSpace(nodeText(node))
		if code != "" {
			b.chapter.Blocks = append(b.chapter.Blocks, Block{Kind: BlockKindCode, Text: code})
		}
	case "ul", "ol":
		items := b.collectListItems(node)
		if len(items) > 0 {
			b.chapter.Blocks = append(b.chapter.Blocks, Block{Kind: BlockKindList, Items: items, Ordered: node.Data == "ol"})
		}
	case "table":
		rows := b.collectTable(node)
		if len(rows) > 0 {
			b.chapter.Blocks = append(b.chapter.Blocks, Block{Kind: BlockKindTable, Rows: rows})
		}
	case "hr":
		b.chapter.Blocks = append(b.chapter.Blocks, Block{Kind: BlockKindSeparator})
	case "section", "article", "div", "main", "body":
		b.consumeChildren(node)
	default:
		text := strings.TrimSpace(b.inlineText(node))
		if text != "" && isStandaloneBlock(node) {
			b.appendParagraph(text)
			return
		}
		b.consumeChildren(node)
	}
}

func (b *chapterBuilder) appendParagraph(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(b.chapter.Blocks) > 0 {
		last := &b.chapter.Blocks[len(b.chapter.Blocks)-1]
		if last.Kind == BlockKindParagraph && shouldMergeParagraph(last.Text, text) {
			last.Text = mergeParagraphs(last.Text, text)
			return
		}
	}
	b.chapter.Blocks = append(b.chapter.Blocks, Block{Kind: BlockKindParagraph, Text: text})
}

func (b *chapterBuilder) inlineText(node *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			text := normalizeInlineText(current.Data)
			if text != "" {
				parts = append(parts, text)
			}
			return
		}
		if current.Type != html.ElementNode {
			return
		}
		if current.Data == "img" || current.Data == "svg" {
			return
		}
		if current.Data == "a" {
			href := attr(current, "href")
			if def, ok := b.resolveFootnote(href); ok {
				label := strings.TrimSpace(nodeText(current))
				if label == "" {
					label = fmt.Sprintf("%d", len(b.footnotes)+1)
				}
				index, ok := b.footnoteMap[def.Key]
				if !ok {
					index = len(b.footnotes) + 1
					b.footnoteMap[def.Key] = index
					if def.SourceRef != "" && def.SourceRef != b.chapter.SourceRef {
						b.chapter.crossFileNotes++
					}
					b.footnotes = append(b.footnotes, Footnote{
						ID:      def.Key,
						Label:   fmt.Sprintf("%d", index),
						Content: cleanFootnoteContentV2(def.Content),
					})
				} else if b.footnotes[index-1].Content == "" && def.Content != "" {
					b.footnotes[index-1].Content = cleanFootnoteContentV2(def.Content)
				}
				parts = append(parts, fmt.Sprintf("[^%d]", index))
				return
			}
		}
		if isNoteNode(current) {
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if current.Data == "br" {
			parts = append(parts, "\n")
		}
	}
	walk(node)
	return joinInlineParts(parts)
}

func (b *chapterBuilder) captureFootnoteNode(node *html.Node) {
	id := attr(node, "id")
	if id == "" {
		id = fmt.Sprintf("note-%d", len(b.footnotes)+1)
	}
	key := b.chapter.SourceRef + "#" + id
	content := strings.TrimSpace(b.inlineText(node))
	if content == "" {
		content = strings.TrimSpace(nodeText(node))
	}
	if content == "" {
		return
	}
	index, ok := b.footnoteMap[key]
	if !ok {
		index = len(b.footnotes) + 1
		b.footnoteMap[key] = index
		b.footnotes = append(b.footnotes, Footnote{
			ID:      key,
			Label:   fmt.Sprintf("%d", index),
			Content: cleanFootnoteContentV2(content),
		})
		return
	}
	b.footnotes[index-1].Content = cleanFootnoteContentV2(content)
}

func (b *chapterBuilder) resolveFootnote(href string) (noteDefinition, bool) {
	if def, ok := b.noteLookup.lookup(b.chapter.SourceRef, href); ok {
		return def, true
	}
	if isFootnoteHref(href, b.noteTargets) {
		key := resolveNoteKey(b.chapter.SourceRef, href)
		return noteDefinition{
			Key:       key,
			SourceRef: b.chapter.SourceRef,
		}, key != ""
	}
	return noteDefinition{}, false
}

func (b *chapterBuilder) collectListItems(node *html.Node) []string {
	var items []string
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "li" {
			text := strings.TrimSpace(b.inlineText(child))
			if text != "" {
				items = append(items, text)
			}
		}
	}
	return items
}

func (b *chapterBuilder) collectTable(node *html.Node) [][]string {
	var rows [][]string
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.ElementNode && current.Data == "tr" {
			var row []string
			for cell := current.FirstChild; cell != nil; cell = cell.NextSibling {
				if cell.Type == html.ElementNode && (cell.Data == "td" || cell.Data == "th") {
					row = append(row, strings.TrimSpace(b.inlineText(cell)))
				}
			}
			if len(row) > 0 {
				rows = append(rows, row)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return rows
}

func collectNoteTargets(node *html.Node, targets map[string]struct{}) {
	if node.Type == html.ElementNode && isNoteNode(node) {
		if id := attr(node, "id"); id != "" {
			targets[id] = struct{}{}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectNoteTargets(child, targets)
	}
}

func isNoteNode(node *html.Node) bool {
	if node == nil || node.Type != html.ElementNode {
		return false
	}
	value := strings.ToLower(attr(node, "epub:type") + " " + attr(node, "type") + " " + attr(node, "class") + " " + attr(node, "role"))
	id := strings.ToLower(attr(node, "id"))
	if strings.Contains(value, "footnote") || strings.Contains(value, "endnote") || strings.Contains(value, "rearnote") {
		return true
	}
	return strings.Contains(id, "footnote") || strings.HasPrefix(id, "fn") || strings.HasPrefix(id, "note")
}

func isFootnoteHref(href string, targets map[string]struct{}) bool {
	id := fragmentID(href)
	if id == "" {
		return false
	}
	_, ok := targets[id]
	return ok
}

func fragmentID(href string) string {
	if !strings.Contains(href, "#") {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(href, "#", 2)[1])
}

func normalizeInlineText(s string) string {
	s = html.UnescapeString(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff':
			return -1
		case '\u00a0', '\u3000':
			return ' '
		default:
			return r
		}
	}, s)
	s = whitespaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func joinInlineParts(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part == "\n" {
			if len(out) == 0 || out[len(out)-1] == "\n" {
				continue
			}
			out = append(out, "\n")
			continue
		}
		if len(out) == 0 || out[len(out)-1] == "\n" {
			out = append(out, part)
			continue
		}
		last := out[len(out)-1]
		if shouldInsertInlineSpace(last, part) {
			out = append(out, " ", part)
			continue
		}
		out[len(out)-1] = last + part
	}

	raw := strings.Join(out, "")
	raw = strings.ReplaceAll(raw, " \n", "\n")
	raw = strings.ReplaceAll(raw, "\n ", "\n")
	return strings.TrimSpace(raw)
}

func shouldInsertInlineSpace(prev, next string) bool {
	if prev == "" || next == "" {
		return false
	}
	if strings.HasPrefix(next, "[^") || strings.HasSuffix(prev, "[") {
		return false
	}

	prevRunes := []rune(prev)
	nextRunes := []rune(next)
	lastPrev := prevRunes[len(prevRunes)-1]
	firstNext := nextRunes[0]

	switch {
	case lastPrev == '\n' || firstNext == '\n':
		return false
	case strings.HasSuffix(prev, "-"):
		return false
	case shouldDropSpaceV2(lastPrev, firstNext):
		return false
	default:
		return true
	}
}

func nodeText(node *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			parts = append(parts, current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return normalizeInlineText(joinInlineParts(parts))
}

func attr(node *html.Node, name string) string {
	for _, item := range node.Attr {
		if item.Key == name {
			return item.Val
		}
	}
	return ""
}

func findElement(node *html.Node, tag string) *html.Node {
	if node.Type == html.ElementNode && node.Data == tag {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findElement(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func isStandaloneBlock(node *html.Node) bool {
	switch node.Data {
	case "aside", "nav", "header", "footer":
		return false
	default:
		return true
	}
}

func shouldMergeParagraph(prev, next string) bool {
	prev = strings.TrimSpace(prev)
	next = strings.TrimSpace(next)
	if prev == "" || next == "" {
		return false
	}
	if strings.HasSuffix(prev, "-") {
		return true
	}
	if containsCJK(prev) || containsCJK(next) {
		return false
	}
	if endsWithSentenceBoundary(prev) {
		return false
	}
	return endsWithASCIIWord(prev) && startsWithASCIILower(next)
}

func mergeParagraphs(prev, next string) string {
	if strings.HasSuffix(prev, "-") {
		return strings.TrimSuffix(prev, "-") + strings.TrimLeft(next, " ")
	}
	if stringsHasCJKBoundary(prev, next) {
		return prev + next
	}
	return prev + " " + next
}

func stringsHasCJKBoundary(prev, next string) bool {
	last := []rune(prev)
	first := []rune(next)
	if len(last) == 0 || len(first) == 0 {
		return false
	}
	return shouldDropSpaceV2(last[len(last)-1], first[0])
}

func startsWithASCIILower(s string) bool {
	runes := []rune(strings.TrimSpace(s))
	return len(runes) > 0 && runes[0] >= 'a' && runes[0] <= 'z'
}

func endsWithASCIIWord(s string) bool {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) == 0 {
		return false
	}
	last := runes[len(runes)-1]
	return unicode.IsLetter(last) || unicode.IsDigit(last)
}

func endsWithSentenceBoundary(s string) bool {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) == 0 {
		return false
	}
	switch runes[len(runes)-1] {
	case '.', '!', '?', ':', ';', ',', ')', ']', '"', '\'', '\u3002', '\uff01', '\uff1f', '\uff1b', '\uff1a', '\uff0c', '\u3001':
		return true
	default:
		return false
	}
}

func isCJKRune(r rune) bool {
	return r >= 0x2E80 && r <= 0x9FFF
}

func isCJKPunctuation(r rune) bool {
	switch {
	case r >= 0x3000 && r <= 0x303F:
		return true
	case r >= 0xFF00 && r <= 0xFFEF:
		return true
	default:
		return false
	}
}

func containsCJK(s string) bool {
	for _, r := range s {
		if isCJKRune(r) {
			return true
		}
	}
	return false
}
