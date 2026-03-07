package rag

type ChapterKind string

const (
	ChapterKindMain        ChapterKind = "main"
	ChapterKindFrontMatter ChapterKind = "frontmatter"
	ChapterKindBackMatter  ChapterKind = "backmatter"
)

type BlockKind string

const (
	BlockKindHeading    BlockKind = "heading"
	BlockKindParagraph  BlockKind = "paragraph"
	BlockKindBlockquote BlockKind = "blockquote"
	BlockKindCode       BlockKind = "code"
	BlockKindList       BlockKind = "list"
	BlockKindTable      BlockKind = "table"
	BlockKindSeparator  BlockKind = "separator"
)
