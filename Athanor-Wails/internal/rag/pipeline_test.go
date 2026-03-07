package rag

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConvertEPUBWritesDiagnostics(t *testing.T) {
	workDir := testOutputDir(t, "diagnostics")
	input := filepath.Join(workDir, "sample.epub")
	createRAGTestEPUB(t, input)

	result, err := ConvertEPUB(context.Background(), input, Options{
		OutputRootDir: workDir,
		BaseName:      "sample",
	})
	if err != nil {
		t.Fatalf("ConvertEPUB failed: %v", err)
	}

	data, err := os.ReadFile(result.DiagnosticsPath)
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}

	var diagnostics Diagnostics
	if err := json.Unmarshal(data, &diagnostics); err != nil {
		t.Fatalf("unmarshal diagnostics: %v", err)
	}

	if diagnostics.Summary.ChunkCount == 0 {
		t.Fatal("expected chunk count in diagnostics")
	}
	if len(diagnostics.Chapters) == 0 {
		t.Fatal("expected chapter diagnostics")
	}
}

func TestConvertEPUBTrimsTOCResidualAndLinksCrossFileFootnotes(t *testing.T) {
	workDir := testOutputDir(t, "toc-footnotes")
	input := filepath.Join(workDir, "toc-footnotes.epub")
	createRAGTestEPUB(t, input)

	result, err := ConvertEPUB(context.Background(), input, Options{
		OutputRootDir: workDir,
		BaseName:      "toc-footnotes",
	})
	if err != nil {
		t.Fatalf("ConvertEPUB failed: %v", err)
	}

	mainData, err := os.ReadFile(result.MainMarkdownPath)
	if err != nil {
		t.Fatalf("read main markdown: %v", err)
	}
	mainText := string(mainData)
	if strings.Contains(mainText, "Contents .... 1") {
		t.Fatalf("toc residue should be trimmed: %s", mainText)
	}
	if !strings.Contains(mainText, "[^1]: This note lives in a separate file.") {
		t.Fatalf("expected cross-file footnote content: %s", mainText)
	}

	data, err := os.ReadFile(result.DiagnosticsPath)
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	var diagnostics Diagnostics
	if err := json.Unmarshal(data, &diagnostics); err != nil {
		t.Fatalf("unmarshal diagnostics: %v", err)
	}
	if diagnostics.Summary.TOCResidualBlocksRemoved == 0 {
		t.Fatal("expected toc cleanup diagnostics")
	}
	if diagnostics.Summary.CrossFileFootnotesLinked == 0 {
		t.Fatal("expected cross-file footnote diagnostics")
	}
}

func createRAGTestEPUB(t *testing.T, output string) {
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
    <dc:title>Sample Book</dc:title>
    <dc:creator>Test Author</dc:creator>
    <dc:language>en</dc:language>
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
      <navLabel><text>Introduction</text></navLabel>
      <content src="chap1.xhtml#intro"/>
    </navPoint>
    <navPoint id="navPoint-2" playOrder="2">
      <navLabel><text>Notes</text></navLabel>
      <content src="notes.xhtml"/>
    </navPoint>
  </navMap>
</ncx>`)
	writeDeflated("OEBPS/chap1.xhtml", `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
  <body>
    <p>Contents .... 1</p>
    <p>Chapter 1 .... 3</p>
    <h1 id="intro">Introduction</h1>
    <p>This is the first paragraph.<a href="notes.xhtml#fn1">1</a></p>
    <p>This is the second paragraph and it is long enough to help chunking stay useful.</p>
  </body>
</html>`)
	writeDeflated("OEBPS/notes.xhtml", `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
  <body>
    <h1>Notes</h1>
    <aside id="fn1" epub:type="footnote">This note lives in a separate file.</aside>
  </body>
</html>`)

	if err := writer.Close(); err != nil {
		t.Fatalf("close epub writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close epub file: %v", err)
	}
}
