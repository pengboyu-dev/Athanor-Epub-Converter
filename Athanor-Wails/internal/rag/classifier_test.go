package rag

import "testing"

func TestClassifyChapterIgnoresSourceRefKeywords(t *testing.T) {
	chapter := Chapter{
		Title:     "Chapter 1: Introduction",
		SourceRef: "OEBPS/index_split_001.xhtml",
	}

	classifyChapter(&chapter, nil)

	if chapter.Kind != ChapterKindMain {
		t.Fatalf("expected main, got %s", chapter.Kind)
	}
	if chapter.ClassifyReason != "default:no_signal" {
		t.Fatalf("unexpected classify reason: %s", chapter.ClassifyReason)
	}
}

func TestClassifyChapterGuideWins(t *testing.T) {
	chapter := Chapter{
		Title:     "Chapter 1: Introduction",
		SourceRef: "OEBPS/cover.xhtml",
	}
	guide := []guideRefXML{
		{Type: "cover", Href: "cover.xhtml"},
	}

	classifyChapter(&chapter, guide)

	if chapter.Kind != ChapterKindFrontMatter {
		t.Fatalf("expected frontmatter, got %s", chapter.Kind)
	}
	if chapter.ClassifyReason != "guide:cover" {
		t.Fatalf("unexpected classify reason: %s", chapter.ClassifyReason)
	}
}

func TestClassifyChapterNotesTitleStaysMain(t *testing.T) {
	chapter := Chapter{
		Title:     "Notes on the Translation",
		SourceRef: "OEBPS/chapter5.xhtml",
	}

	classifyChapter(&chapter, nil)

	if chapter.Kind != ChapterKindMain {
		t.Fatalf("expected main, got %s", chapter.Kind)
	}
}

func TestClassifyChapterSpecificBackmatterTitles(t *testing.T) {
	tests := []struct {
		title string
		want  ChapterKind
	}{
		{title: "Works Cited", want: ChapterKindBackMatter},
		{title: "Translation Notes", want: ChapterKindBackMatter},
		{title: "Translator's Afterword:", want: ChapterKindBackMatter},
	}

	for _, tt := range tests {
		chapter := Chapter{Title: tt.title, SourceRef: "OEBPS/notes.xhtml"}
		classifyChapter(&chapter, nil)
		if chapter.Kind != tt.want {
			t.Fatalf("%s: expected %s, got %s", tt.title, tt.want, chapter.Kind)
		}
	}
}

func TestValidateClassificationFallback(t *testing.T) {
	book := Book{
		Back: []Chapter{
			{ID: "front", Kind: ChapterKindFrontMatter, Order: 1},
			{ID: "body", Kind: ChapterKindBackMatter, Order: 2},
		},
	}

	validateClassification(&book)

	if len(book.Main) != 1 {
		t.Fatalf("expected 1 main chapter, got %d", len(book.Main))
	}
	if book.Main[0].ID != "body" {
		t.Fatalf("unexpected moved chapter: %s", book.Main[0].ID)
	}
	if book.Main[0].ClassifyReason != "fallback:no_main_detected" {
		t.Fatalf("unexpected fallback reason: %s", book.Main[0].ClassifyReason)
	}
	if len(book.Back) != 1 || book.Back[0].ID != "front" {
		t.Fatalf("frontmatter should stay in back bucket: %+v", book.Back)
	}
}
