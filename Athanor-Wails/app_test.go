package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"Athanor-Wails/internal/rag"
)

func TestOutputPathBase(t *testing.T) {
	got := outputPathBase(`D:\books\测试.epub`)
	if got != "测试_athanor" {
		t.Fatalf("unexpected output base: %s", got)
	}
}

func TestConvertEPUB(t *testing.T) {
	workDir := filepath.Join(".", ".tmp", "test-convert")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}
	input := filepath.Join(workDir, "sample.epub")
	createSampleEPUB(t, input)

	result, err := rag.ConvertEPUB(context.Background(), input, rag.Options{
		OutputRootDir: workDir,
		BaseName:      "sample_athanor",
	})
	if err != nil {
		t.Fatalf("ConvertEPUB failed: %v", err)
	}

	mainData, err := os.ReadFile(result.MainMarkdownPath)
	if err != nil {
		t.Fatalf("read main markdown: %v", err)
	}
	mainText := string(mainData)
	if !strings.Contains(mainText, "# 示例图书") {
		t.Fatalf("missing book title in markdown: %s", mainText)
	}
	if !strings.Contains(mainText, "这是第一段中文内容。") {
		t.Fatalf("missing chapter content: %s", mainText)
	}
	if !strings.Contains(mainText, "这是附录内容。") {
		t.Fatalf("missing backmatter content: %s", mainText)
	}
	if strings.Contains(mainText, "ATHANOR") || strings.Contains(mainText, "文档元数据") || strings.Contains(mainText, "文件哈希") {
		t.Fatalf("main markdown should not contain wrapper metadata: %s", mainText)
	}

	debugData, err := os.ReadFile(result.DebugMarkdownPath)
	if err != nil {
		t.Fatalf("read debug markdown: %v", err)
	}
	debugText := string(debugData)
	if !strings.Contains(debugText, "## Debug") || !strings.Contains(debugText, "- source_ref:") {
		t.Fatalf("debug markdown missing debug fields: %s", debugText)
	}

	chapterPath := filepath.Join(result.ArtifactDir, "chapters", "chapter-001.md")
	chapterData, err := os.ReadFile(chapterPath)
	if err != nil {
		t.Fatalf("read chapter markdown: %v", err)
	}
	chapterText := string(chapterData)
	if !strings.Contains(chapterText, "## 脚注") {
		t.Fatalf("expected footnotes in chapter markdown")
	}
	if strings.Contains(chapterText, "ATHANOR CHAPTER") || strings.Contains(chapterText, "- source:") || strings.Contains(chapterText, "- classify:") {
		t.Fatalf("chapter markdown should not contain wrapper metadata: %s", chapterText)
	}

	metadataData, err := os.ReadFile(result.MetadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var metadata rag.Metadata
	if err := json.Unmarshal(metadataData, &metadata); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if metadata.Title != "示例图书" {
		t.Fatalf("unexpected metadata title: %s", metadata.Title)
	}

	chunksData, err := os.ReadFile(result.ChunksPath)
	if err != nil {
		t.Fatalf("read chunks: %v", err)
	}
	if len(strings.TrimSpace(string(chunksData))) == 0 {
		t.Fatal("chunks.jsonl should not be empty")
	}
}

func createSampleEPUB(t *testing.T, output string) {
	t.Helper()

	file, err := os.Create(output)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}

	writer := zip.NewWriter(file)
	writeStored := func(name, content string) {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create stored entry %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write stored entry %s: %v", name, err)
		}
	}
	writeDeflated := func(name, content string) {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create entry %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
		}
	}

	writeStored("mimetype", "application/epub+zip")
	writeDeflated("META-INF/container.xml", `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)
	writeDeflated("OEBPS/content.opf", `<?xml version="1.0" encoding="UTF-8"?>
<package version="2.0" xmlns="http://www.idpf.org/2007/opf" unique-identifier="BookId">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>示例图书</dc:title>
    <dc:creator>测试作者</dc:creator>
    <dc:language>zh-CN</dc:language>
    <dc:identifier id="BookId">urn:uuid:1234</dc:identifier>
  </metadata>
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="chap1" href="chap1.xhtml" media-type="application/xhtml+xml"/>
    <item id="notes" href="notes.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine toc="ncx">
    <itemref idref="chap1"/>
    <itemref idref="notes"/>
  </spine>
</package>`)
	writeDeflated("OEBPS/toc.ncx", `<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <navMap>
    <navPoint id="navPoint-1" playOrder="1">
      <navLabel><text>第一章</text></navLabel>
      <content src="chap1.xhtml"/>
    </navPoint>
    <navPoint id="navPoint-2" playOrder="2">
      <navLabel><text>附录</text></navLabel>
      <content src="notes.xhtml"/>
    </navPoint>
  </navMap>
</ncx>`)
	writeDeflated("OEBPS/chap1.xhtml", `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
  <body>
    <h1>第一章</h1>
    <p>这是第一段中文内容。<a href="#fn1">1</a></p>
    <p>这是第二段中文内容。</p>
    <aside id="fn1" epub:type="footnote">这是脚注内容。</aside>
  </body>
</html>`)
	writeDeflated("OEBPS/notes.xhtml", `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
  <body>
    <h1>附录</h1>
    <p>这是附录内容。</p>
  </body>
</html>`)

	if err := writer.Close(); err != nil {
		t.Fatalf("close epub writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close epub file: %v", err)
	}
}
