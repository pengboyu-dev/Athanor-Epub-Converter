# Athanor EPUB Converter

[简体中文](./README.zh-CN.md) · [MIT License](./LICENSE)

📘 Convert EPUB into RAG-ready Markdown.

Athanor EPUB Converter focuses on one thing: turning EPUB files into clean Markdown for retrieval, knowledge bases, and downstream processing, instead of continuing along older PDF / LaTeX / Pandoc-oriented pipelines.

## Overview

This project is designed for EPUB-to-Markdown conversion workflows where the goal is clean, structured, retrieval-ready text rather than layout-oriented publishing output.

## Current Focus

- Parse EPUB containers, OPF, and NCX / Nav TOC
- Organize content into `main / frontmatter / backmatter`
- Clean TOC residue, duplicate blocks, and footnote noise
- Output clean main Markdown, chapter Markdown, and `chunks.jsonl`
- Generate `diagnostics.json` and `debug.md` for troubleshooting
- Provide batch regression baselines and minimal retrieval evaluation

## Repository Layout

```text
Athanor-Wails/
  app.go                         Wails shell layer
  main.go                        Application entry
  internal/rag/                  Core EPUB -> RAG Markdown pipeline
  cmd/build-regression-baseline/ Batch baseline generator
  frontend/                      Wails frontend
```

## Outputs

A single conversion produces the following main artifacts:

- `<BaseName>.md`  
  Clean primary document. By default, the main body is kept free of product markers, paths, hashes, or debug annotations.

- `<BaseName>/chapters/*.md`  
  Chapter-split Markdown files.

- `<BaseName>/chunks.jsonl`  
  Chunked output for RAG workflows.

- `<BaseName>/diagnostics.json`  
  Statistics and anomaly warnings.

- `<BaseName>/debug.md`  
  Debug export for troubleshooting only.

## Development

### Requirements

- Go 1.21+
- Wails v2
- Node.js

### Development Mode

```bash
wails dev
```

### Build

```bash
wails build
```

### Test

```bash
go test ./...
```

### Generate Batch Regression Baselines

```bash
go run ./cmd/build-regression-baseline
```

## Notes

- The main pipeline is now pure Go; Wails only remains as the desktop shell layer.
- The primary `md` output aims to stay clean and readable; debug information is separated into `debug.md`.
- This pipeline is currently designed for RAG-oriented Markdown, not layout-oriented publishing output.

## Status

This project is currently in early beta:

- The main pipeline runs.
- Regression, diagnostics, and minimal evaluation already have a working foundation.
- Chunking strategy, evaluation loop, and batch workflow are still being refined.
