# Athanor EPUB Converter

[English](./README.md) · [MIT License](./LICENSE)

📘 EPUB -> RAG Markdown 转换器。

Athanor EPUB Converter 当前聚焦一件事：把 EPUB 转成适合检索、知识库和后续加工的干净 Markdown 原料，而不是继续走旧的 PDF / LaTeX / Pandoc 导向链路。

## 项目简介

这个项目面向 EPUB 到 Markdown 的转换场景，目标不是排版导向输出，而是获得干净、结构化、适合检索与后续处理的文本结果。

## 当前重点

- 解析 EPUB 容器、OPF、NCX / Nav TOC
- 将内容组织为 `main / frontmatter / backmatter`
- 清洗目录残留、重复块和脚注噪音
- 输出干净主 Markdown、章节 Markdown 与 `chunks.jsonl`
- 生成 `diagnostics.json` 与 `debug.md` 便于排查
- 提供批量回归基线和最小检索评测

## 仓库结构

```text
Athanor-Wails/
  app.go                         Wails 壳层
  main.go                        应用入口
  internal/rag/                  EPUB -> RAG Markdown 核心链路
  cmd/build-regression-baseline/ 批量基线生成器
  frontend/                      Wails 前端
```

## 输出产物

一次转换会生成这些主要产物：

- `<BaseName>.md`  
  干净主文档。默认不在正文中混入产品标记、路径、哈希或调试注释。

- `<BaseName>/chapters/*.md`  
  按章节拆开的 Markdown。

- `<BaseName>/chunks.jsonl`  
  面向 RAG 的分块结果。

- `<BaseName>/diagnostics.json`  
  统计信息与异常告警。

- `<BaseName>/debug.md`  
  仅供排查问题使用的调试导出。

## 开发

### 环境要求

- Go 1.21+
- Wails v2
- Node.js

### 开发模式

```bash
wails dev
```

### 构建

```bash
wails build
```

### 测试

```bash
go test ./...
```

### 生成批量回归基线

```bash
go run ./cmd/build-regression-baseline
```

## 说明

- 主流程现在是纯 Go，Wails 只保留桌面壳层职责。
- 主 `md` 追求干净可读；调试信息单独放在 `debug.md`。
- 这条链路当前服务于 RAG Markdown，而不是排版导向输出。

## 状态

当前阶段可视为 early beta：

- 主链路可跑。
- 回归、诊断、最小评测已具备基础骨架。
- 仍在继续打磨 chunk 策略、评测闭环和批量体验。
