package rag

import (
	"bytes"
	"path"
	"strings"

	"golang.org/x/net/html"
)

type noteDefinition struct {
	Key       string
	SourceRef string
	Content   string
}

type noteRegistry struct {
	byKey map[string]noteDefinition
}

func buildNoteRegistry(entries map[string]zipEntry, opfDir string, pkg packageXML) noteRegistry {
	registry := noteRegistry{byKey: map[string]noteDefinition{}}
	for _, item := range pkg.Manifest.Items {
		if !isXHTMLItem(item.MediaType, item.Href) {
			continue
		}
		full := resolveHref(opfDir, item.Href)
		entry, ok := entries[full]
		if !ok {
			continue
		}
		defs := collectNoteDefinitions(full, entry.data)
		for key, def := range defs {
			registry.byKey[key] = def
		}
	}
	return registry
}

func collectNoteDefinitions(sourceRef string, data []byte) map[string]noteDefinition {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	body := findElement(doc, "body")
	if body == nil {
		return nil
	}

	defs := map[string]noteDefinition{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if isNoteNode(node) {
			id := strings.TrimSpace(attr(node, "id"))
			if id != "" {
				key := sourceRef + "#" + id
				content := cleanFootnoteContentV2(nodeText(node))
				if content != "" {
					defs[key] = noteDefinition{
						Key:       key,
						SourceRef: sourceRef,
						Content:   content,
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(body)
	return defs
}

func isXHTMLItem(mediaType, href string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "application/xhtml+xml", "text/html":
		return true
	}
	lowerHref := strings.ToLower(href)
	return strings.HasSuffix(lowerHref, ".xhtml") || strings.HasSuffix(lowerHref, ".html") || strings.HasSuffix(lowerHref, ".htm")
}

func resolveNoteKey(currentRef, href string) string {
	id := fragmentID(href)
	if id == "" {
		return ""
	}
	base := strings.SplitN(currentRef, "#", 2)[0]
	targetBase := resolveHref(path.Dir(base), href)
	if targetBase == "" {
		targetBase = base
	}
	return targetBase + "#" + id
}

func (r noteRegistry) lookup(currentRef, href string) (noteDefinition, bool) {
	if len(r.byKey) == 0 {
		return noteDefinition{}, false
	}
	key := resolveNoteKey(currentRef, href)
	if key == "" {
		return noteDefinition{}, false
	}
	def, ok := r.byKey[key]
	return def, ok
}

func cleanFootnoteContentV2(text string) string {
	text = normalizeParagraphV2(text)
	text = strings.TrimPrefix(text, "↩")
	text = strings.TrimPrefix(text, "↑")
	text = strings.TrimPrefix(text, "^")
	return strings.TrimSpace(text)
}
