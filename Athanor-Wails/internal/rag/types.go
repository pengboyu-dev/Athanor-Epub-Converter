package rag

import "context"

type Options struct {
	OutputRootDir string
	BaseName      string
	Logger        func(string)
	Progress      func(stage string, pct float64, message string)
	Context       context.Context
	ChunkConfig   ChunkConfig
}

type ChunkConfig struct {
	IncludeBackmatter bool `json:"includeBackmatter,omitempty"`
	TargetSize        int  `json:"targetSize,omitempty"`
	MinSize           int  `json:"minSize,omitempty"`
	MaxSize           int  `json:"maxSize,omitempty"`
}

type ConvertResult struct {
	MainMarkdownPath string
	DebugMarkdownPath string
	ArtifactDir      string
	MetadataPath     string
	TOCPath          string
	ChunksPath       string
	DiagnosticsPath  string
	Stats            Stats
}

type Stats struct {
	ChapterCount     int `json:"chapterCount"`
	FrontMatterCount int `json:"frontMatterCount"`
	BackMatterCount  int `json:"backMatterCount"`
	ChunkCount       int `json:"chunkCount"`
	FootnoteCount    int `json:"footnoteCount"`
}

type Book struct {
	Metadata Metadata  `json:"metadata"`
	Main     []Chapter `json:"main"`
	Back     []Chapter `json:"back"`
	Stats    Stats     `json:"stats"`
}

type Metadata struct {
	Title         string   `json:"title"`
	Authors       []string `json:"authors,omitempty"`
	Language      string   `json:"language,omitempty"`
	Publisher     string   `json:"publisher,omitempty"`
	PublishedDate string   `json:"publishedDate,omitempty"`
	Identifier    string   `json:"identifier,omitempty"`
	SourcePath    string   `json:"sourcePath"`
	SourceSHA256  string   `json:"sourceSha256"`
}

type Chapter struct {
	ID             string      `json:"id"`
	Title          string      `json:"title"`
	Order          int         `json:"order"`
	Kind           ChapterKind `json:"kind"`
	ClassifyReason string      `json:"classifyReason,omitempty"`
	SourceRef      string      `json:"sourceRef"`
	Blocks         []Block     `json:"blocks"`
	Footnotes      []Footnote  `json:"footnotes,omitempty"`
	tocTrimmed     int
	crossFileNotes int
	warnings       []string
}

type Footnote struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Content string `json:"content"`
}

type Block struct {
	Kind    BlockKind  `json:"kind"`
	Text    string     `json:"text,omitempty"`
	Level   int        `json:"level,omitempty"`
	Items   []string   `json:"items,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
	Ordered bool       `json:"ordered,omitempty"`
}

type TOCItem struct {
	ID             string      `json:"id"`
	Title          string      `json:"title"`
	Kind           ChapterKind `json:"kind"`
	ClassifyReason string      `json:"classifyReason,omitempty"`
	Order          int         `json:"order"`
	Source         string      `json:"source"`
}

type Chunk struct {
	ID            string      `json:"id"`
	BookTitle     string      `json:"bookTitle"`
	ChapterID     string      `json:"chapterId"`
	ChapterTitle  string      `json:"chapterTitle"`
	ChapterOrder  int         `json:"chapterOrder"`
	Kind          ChapterKind `json:"kind"`
	Sequence      int         `json:"sequence"`
	Text          string      `json:"text"`
	SourcePath    string      `json:"sourcePath"`
	SourceRef     string      `json:"sourceRef"`
	CharacterSize int         `json:"characterSize"`
	BlockCount    int         `json:"blockCount,omitempty"`
	HeadingPath   []string    `json:"headingPath,omitempty"`
	Language      string      `json:"language,omitempty"`
	HasFootnotes  bool        `json:"hasFootnotes,omitempty"`
	TokenEstimate int         `json:"tokenEstimate,omitempty"`
}

type Diagnostics struct {
	Summary  DiagnosticsSummary  `json:"summary"`
	Chapters []ChapterDiagnostic `json:"chapters"`
	Chunks   []ChunkDiagnostic   `json:"chunks,omitempty"`
}

type DiagnosticsSummary struct {
	PipelineVersion          string `json:"pipelineVersion"`
	GeneratedAt              string `json:"generatedAt"`
	SourcePath               string `json:"sourcePath"`
	SourceSHA256             string `json:"sourceSha256"`
	Title                    string `json:"title"`
	ChapterCount             int    `json:"chapterCount"`
	FrontMatterCount         int    `json:"frontMatterCount"`
	BackMatterCount          int    `json:"backMatterCount"`
	ChunkCount               int    `json:"chunkCount"`
	FootnoteCount            int    `json:"footnoteCount"`
	TOCResidualBlocksRemoved int    `json:"tocResidualBlocksRemoved"`
	CrossFileFootnotesLinked int    `json:"crossFileFootnotesLinked"`
	ShortChunkCount          int    `json:"shortChunkCount"`
	OversizeChunkCount       int    `json:"oversizeChunkCount"`
	MinChunkCharacters       int    `json:"minChunkCharacters"`
	AverageChunkCharacters   int    `json:"averageChunkCharacters"`
	P50ChunkCharacters       int    `json:"p50ChunkCharacters"`
	P90ChunkCharacters       int    `json:"p90ChunkCharacters"`
	MaxChunkCharacters       int    `json:"maxChunkCharacters"`
}

type ChapterDiagnostic struct {
	ID                       string      `json:"id"`
	Title                    string      `json:"title"`
	Order                    int         `json:"order"`
	Kind                     ChapterKind `json:"kind"`
	ClassifyReason           string      `json:"classifyReason,omitempty"`
	SourceRef                string      `json:"sourceRef"`
	BlockCount               int         `json:"blockCount"`
	FootnoteCount            int         `json:"footnoteCount"`
	ChunkCount               int         `json:"chunkCount"`
	ShortChunkCount          int         `json:"shortChunkCount,omitempty"`
	OversizeChunkCount       int         `json:"oversizeChunkCount,omitempty"`
	MinChunkCharacters       int         `json:"minChunkCharacters,omitempty"`
	AverageChunkCharacters   int         `json:"averageChunkCharacters,omitempty"`
	MaxChunkCharacters       int         `json:"maxChunkCharacters,omitempty"`
	TOCResidualBlocksRemoved int         `json:"tocResidualBlocksRemoved,omitempty"`
	CrossFileFootnotesLinked int         `json:"crossFileFootnotesLinked,omitempty"`
	Warnings                 []string    `json:"warnings,omitempty"`
}

type ChunkDiagnostic struct {
	ID            string      `json:"id"`
	ChapterID     string      `json:"chapterId"`
	ChapterTitle  string      `json:"chapterTitle"`
	ChapterOrder  int         `json:"chapterOrder"`
	Kind          ChapterKind `json:"kind"`
	Sequence      int         `json:"sequence"`
	CharacterSize int         `json:"characterSize"`
	BlockCount    int         `json:"blockCount,omitempty"`
	HeadingPath   []string    `json:"headingPath,omitempty"`
	HasFootnotes  bool        `json:"hasFootnotes,omitempty"`
	TokenEstimate int         `json:"tokenEstimate,omitempty"`
	Warnings      []string    `json:"warnings,omitempty"`
}
