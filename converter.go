package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ────────────────────────────────────────────────────────────────
// Markdown (AI-ready)
//
//	pandoc book.epub
//	  -t gfm
//	  --wrap=none
//	  --extract-media=book_media
//	  -o book.md
//
// ────────────────────────────────────────────────────────────────
func convertToMarkdown(ctx context.Context, epubPath string) error {
	dir := filepath.Dir(epubPath)
	name := filepath.Base(epubPath)
	base := strings.TrimSuffix(name, filepath.Ext(name))

	args := []string{
		name,
		"-t", "gfm",
		"--wrap=none",
		"--extract-media=" + base + "_media",
		"-o", base + ".md",
	}

	cmd := exec.CommandContext(ctx, "pandoc", args...)
	cmd.Dir = dir
	hideWindow(cmd)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, bytes2str(out))
	}
	return nil
}

// ────────────────────────────────────────────────────────────────
// PDF (Human-ready, Chinese / Windows)
//
//	pandoc book.epub
//	  -o book.pdf
//	  --pdf-engine=xelatex
//	  -V CJKmainfont="Microsoft YaHei"   ← critical
//	  -V mainfont="Microsoft YaHei"
//	  …
//	  --include-in-header=<tmp>.tex       ← \renewcommand{\maketitle}{}
//
// ────────────────────────────────────────────────────────────────
func convertToPDF(ctx context.Context, epubPath string) error {
	dir := filepath.Dir(epubPath)
	name := filepath.Base(epubPath)
	base := strings.TrimSuffix(name, filepath.Ext(name))

	// ── temp LaTeX header: suppress the automatic title page ──
	tmpHeader, err := os.CreateTemp("", "epub-conv-*.tex")
	if err != nil {
		return fmt.Errorf("create temp header: %w", err)
	}
	tmpPath := tmpHeader.Name()
	defer os.Remove(tmpPath)

	const latexPreamble = `% suppress duplicate cover / title page
\renewcommand{\maketitle}{}
`
	if _, err := tmpHeader.WriteString(latexPreamble); err != nil {
		tmpHeader.Close()
		return fmt.Errorf("write temp header: %w", err)
	}
	tmpHeader.Close()

	args := []string{
		name,
		"-o", base + ".pdf",
		"--pdf-engine=xelatex",
		// ── CJK font mapping (xeCJK auto-loaded by pandoc) ──
		"-V", "CJKmainfont=Microsoft YaHei",
		"-V", "CJKsansfont=Microsoft YaHei",
		"-V", "CJKmonofont=Microsoft YaHei",
		// ── Latin font mapping ──
		"-V", "mainfont=Microsoft YaHei",
		"-V", "sansfont=Microsoft YaHei",
		"-V", "monofont=Consolas",
		// ── Page geometry ──
		"-V", "geometry:margin=2.5cm",
		// ── Inject the preamble ──
		"--include-in-header=" + tmpPath,
	}

	cmd := exec.CommandContext(ctx, "pandoc", args...)
	cmd.Dir = dir
	hideWindow(cmd)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, bytes2str(out))
	}
	return nil
}

// bytes2str trims trailing whitespace from command output.
func bytes2str(b []byte) string {
	return strings.TrimSpace(string(b))
}
