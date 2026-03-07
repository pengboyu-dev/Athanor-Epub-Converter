package rag

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"

	"golang.org/x/net/html"
)

func ParseEPUB(ctx context.Context, inputPath string) (Book, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	reader, err := zip.OpenReader(inputPath)
	if err != nil {
		return Book{}, fmt.Errorf("打开 EPUB 失败: %w", err)
	}
	defer reader.Close()

	entries := map[string]zipEntry{}
	for _, file := range reader.File {
		rc, err := file.Open()
		if err != nil {
			return Book{}, fmt.Errorf("读取 EPUB 条目失败: %w", err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return Book{}, fmt.Errorf("读取 EPUB 条目失败: %w", err)
		}
		entries[file.Name] = zipEntry{name: file.Name, data: data}
	}

	containerData, ok := entries["META-INF/container.xml"]
	if !ok {
		return Book{}, fmt.Errorf("缺少 META-INF/container.xml")
	}

	var container containerXML
	if err := decodeXML(containerData.data, &container); err != nil {
		return Book{}, fmt.Errorf("解析 container.xml 失败: %w", err)
	}
	if len(container.Rootfiles) == 0 {
		return Book{}, fmt.Errorf("EPUB 缺少 rootfile")
	}

	opfPath := container.Rootfiles[0].FullPath
	opfEntry, ok := entries[opfPath]
	if !ok {
		return Book{}, fmt.Errorf("找不到 OPF: %s", opfPath)
	}

	var pkg packageXML
	if err := decodeXML(opfEntry.data, &pkg); err != nil {
		return Book{}, fmt.Errorf("解析 OPF 失败: %w", err)
	}

	book := Book{
		Metadata: Metadata{
			Title:         firstNonEmpty(pkg.Metadata.Title...),
			Authors:       filterNonEmpty(pkg.Metadata.Creator),
			Language:      firstNonEmpty(pkg.Metadata.Language...),
			Publisher:     firstNonEmpty(pkg.Metadata.Publisher...),
			PublishedDate: firstNonEmpty(pkg.Metadata.Date...),
			Identifier:    firstNonEmpty(pkg.Metadata.Identifier...),
		},
	}

	opfDir := path.Dir(opfPath)
	manifest := map[string]struct {
		Href       string
		Properties string
	}{}
	for _, item := range pkg.Manifest.Items {
		manifest[item.ID] = struct {
			Href       string
			Properties string
		}{
			Href:       resolveHref(opfDir, item.Href),
			Properties: item.Properties,
		}
	}

	tocTargets := extractTOCTargets(entries, opfDir, pkg)
	targetsByHref := groupTOCTargetsByBase(tocTargets)
	noteRegistry := buildNoteRegistry(entries, opfDir, pkg)
	order := 0
	for _, itemref := range pkg.Spine.Itemrefs {
		if err := ctx.Err(); err != nil {
			return Book{}, err
		}
		item, ok := manifest[itemref.IDRef]
		if !ok {
			continue
		}
		entry, ok := entries[item.Href]
		if !ok {
			continue
		}
		chapters, err := parseChapters(entry.name, entry.data, order+1, targetsByHref[item.Href], noteRegistry)
		if err != nil {
			return Book{}, err
		}
		for _, chapter := range chapters {
			order++
			chapter.Order = order
			chapter.ID = fmt.Sprintf("chapter-%03d", order)
			if chapter.ClassifyReason == "" {
				classifyChapter(&chapter, pkg.Guide.Refs)
			}
			if chapter.Kind == ChapterKindMain {
				book.Main = append(book.Main, chapter)
			} else {
				book.Back = append(book.Back, chapter)
			}
		}
	}

	validateClassification(&book)
	sortChaptersByOrder(book.Main)
	sortChaptersByOrder(book.Back)
	recomputeStats(&book)

	return book, nil
}

func parseChapters(sourceRef string, data []byte, startOrder int, targets []tocTarget, notes noteRegistry) ([]Chapter, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("解析 XHTML 失败 (%s): %w", sourceRef, err)
	}

	body := findElement(doc, "body")
	if body == nil {
		return nil, nil
	}

	noteTargets := map[string]struct{}{}
	collectNoteTargets(body, noteTargets)

	if segments := splitBodyByTargets(body, targets); len(segments) > 1 {
		chapters := make([]Chapter, 0, len(segments))
		nextOrder := startOrder
		for _, segment := range segments {
			chapter, ok := buildChapterFromNodes(sourceRef, nextOrder, segment.Title, segment.Nodes, noteTargets, notes)
			if !ok {
				continue
			}
			if segment.ForceKind != "" {
				chapter.Kind = segment.ForceKind
				chapter.ClassifyReason = segment.ForceReason
			}
			chapter.warnings = append(chapter.warnings, segment.Warnings...)
			chapters = append(chapters, chapter)
			nextOrder++
		}
		return chapters, nil
	}

	tocTitle := ""
	if len(targets) > 0 {
		tocTitle = targets[0].Title
	}
	chapter, ok := buildChapterFromNodes(sourceRef, startOrder, tocTitle, bodyChildren(body), noteTargets, notes)
	if !ok {
		return nil, nil
	}
	return []Chapter{chapter}, nil
}

func buildChapterFromNodes(sourceRef string, order int, tocTitle string, nodes []*html.Node, noteTargets map[string]struct{}, notes noteRegistry) (Chapter, bool) {
	builder := newChapterBuilder(sourceRef, order, tocTitle, noteTargets, notes)
	for _, node := range nodes {
		builder.consumeNode(node)
	}
	chapter := builder.build()
	if len(chapter.Blocks) == 0 && len(chapter.Footnotes) == 0 {
		return Chapter{}, false
	}
	return chapter, true
}
