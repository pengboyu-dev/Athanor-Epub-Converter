package rag

import (
	"path"
	"sort"
	"strings"
)

var frontExactTitles = []string{
	"cover",
	"title page",
	"copyright",
	"toc",
	"contents",
	"table of contents",
	"preface",
	"foreword",
	"\u7248\u6743",
	"\u6249\u9875",
	"\u76ee\u5f55",
	"\u524d\u8a00",
	"\u5e8f\u8a00",
}

var frontPrefixTitles = []string{
	"copyright",
	"preface",
	"foreword",
	"\u7248\u6743",
	"\u524d\u8a00",
	"\u5e8f\u8a00",
}

var backExactTitles = []string{
	"appendix",
	"bibliography",
	"references",
	"works cited",
	"glossary",
	"afterword",
	"translation notes",
	"translator's notes",
	"acknowledgment",
	"acknowledgement",
	"\u9644\u5f55",
	"\u53c2\u8003\u6587\u732e",
	"\u540e\u8bb0",
	"\u81f4\u8c22",
	"\u8bcd\u6c47\u8868",
}

var backPrefixTitles = []string{
	"appendix",
	"bibliography",
	"references",
	"works cited",
	"glossary",
	"afterword",
	"translator's afterword",
	"translation notes",
	"translator's notes",
	"acknowledgment",
	"acknowledgement",
	"\u9644\u5f55",
	"\u53c2\u8003\u6587\u732e",
	"\u540e\u8bb0",
	"\u81f4\u8c22",
}

func classifyChapter(chapter *Chapter, guide []guideRefXML) {
	if chapter == nil {
		return
	}

	if kind, reason, ok := classifyByGuide(chapter.SourceRef, guide); ok {
		chapter.Kind = kind
		chapter.ClassifyReason = reason
		return
	}

	title := normalizeTitle(chapter.Title)
	if kind, reason, ok := classifyByTitle(title); ok {
		chapter.Kind = kind
		chapter.ClassifyReason = reason
		return
	}

	chapter.Kind = ChapterKindMain
	chapter.ClassifyReason = "default:no_signal"
}

func classifyByGuide(sourceRef string, guide []guideRefXML) (ChapterKind, string, bool) {
	for _, ref := range guide {
		if !hrefsMatch(sourceRef, ref.Href) {
			continue
		}
		guideType := normalizeTitle(ref.Type)
		switch {
		case guideType == "text" || guideType == "bodymatter":
			return ChapterKindMain, "guide:" + guideType, true
		case hasExactOrPrefix(guideType, []string{"cover", "toc", "title-page", "titlepage", "copyright"}):
			return ChapterKindFrontMatter, "guide:" + guideType, true
		case hasExactOrPrefix(guideType, []string{"appendix", "bibliography", "glossary", "index", "notes", "endnotes", "footnotes"}):
			return ChapterKindBackMatter, "guide:" + guideType, true
		}
	}
	return "", "", false
}

func classifyByTitle(title string) (ChapterKind, string, bool) {
	if title == "" {
		return "", "", false
	}
	if keyword, ok := exactMatch(title, frontExactTitles); ok {
		return ChapterKindFrontMatter, "title_exact:" + keyword, true
	}
	if keyword, ok := exactMatch(title, backExactTitles); ok {
		return ChapterKindBackMatter, "title_exact:" + keyword, true
	}
	if keyword, ok := prefixMatch(title, frontPrefixTitles); ok {
		return ChapterKindFrontMatter, "title_prefix:" + keyword, true
	}
	if keyword, ok := prefixMatch(title, backPrefixTitles); ok {
		return ChapterKindBackMatter, "title_prefix:" + keyword, true
	}
	return "", "", false
}

func validateClassification(book *Book) {
	if book == nil || len(book.Main) > 0 || len(book.Back) == 0 {
		return
	}

	front := make([]Chapter, 0, len(book.Back))
	main := make([]Chapter, 0, len(book.Back))
	for _, chapter := range book.Back {
		if chapter.Kind == ChapterKindFrontMatter {
			front = append(front, chapter)
			continue
		}
		chapter.Kind = ChapterKindMain
		chapter.ClassifyReason = "fallback:no_main_detected"
		main = append(main, chapter)
	}

	if len(main) == 0 {
		return
	}

	book.Main = append(book.Main, main...)
	book.Back = front
	sortChaptersByOrder(book.Main)
	sortChaptersByOrder(book.Back)
}

func sortChaptersByOrder(chapters []Chapter) {
	sort.SliceStable(chapters, func(i, j int) bool {
		return chapters[i].Order < chapters[j].Order
	})
}

func normalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}

func hrefsMatch(sourceRef, href string) bool {
	sourceRef = path.Clean(strings.SplitN(sourceRef, "#", 2)[0])
	href = path.Clean(strings.SplitN(href, "#", 2)[0])
	if href == "." || href == "" {
		return false
	}
	return sourceRef == href || strings.HasSuffix(sourceRef, "/"+href)
}

func exactMatch(value string, keywords []string) (string, bool) {
	for _, keyword := range keywords {
		if value == keyword {
			return keyword, true
		}
	}
	return "", false
}

func prefixMatch(value string, keywords []string) (string, bool) {
	for _, keyword := range keywords {
		if value == keyword {
			return keyword, true
		}
		if strings.HasPrefix(value, keyword+":") || strings.HasPrefix(value, keyword+" ") {
			return keyword, true
		}
	}
	return "", false
}

func hasExactOrPrefix(value string, keywords []string) bool {
	for _, keyword := range keywords {
		if value == keyword || strings.HasPrefix(value, keyword+":") || strings.HasPrefix(value, keyword+"-") {
			return true
		}
	}
	return false
}
