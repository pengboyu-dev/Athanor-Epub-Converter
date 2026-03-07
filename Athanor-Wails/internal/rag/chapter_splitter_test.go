package rag

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestSplitBodyMultiTargetUnderWrapper(t *testing.T) {
	body := parseBodyFromHTML(t, `
		<div class="content">
			<h2 id="ch1">Chapter 1</h2>
			<p>Content 1</p>
			<h2 id="ch2">Chapter 2</h2>
			<p>Content 2</p>
		</div>`)

	targets := []tocTarget{
		{Fragment: "ch1", Title: "Chapter 1", PlayOrder: 1},
		{Fragment: "ch2", Title: "Chapter 2", PlayOrder: 2},
	}

	segments := splitBodyByTargets(body, targets)
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].Title != "Chapter 1" {
		t.Fatalf("segment 0 title: got %q", segments[0].Title)
	}
	if segments[1].Title != "Chapter 2" {
		t.Fatalf("segment 1 title: got %q", segments[1].Title)
	}
}

func TestSplitBodyMultiTargetNestedWrappers(t *testing.T) {
	body := parseBodyFromHTML(t, `
		<div class="outer">
			<div class="inner">
				<section id="a"><h2>A</h2><p>A content</p></section>
				<section id="b"><h2>B</h2><p>B content</p></section>
			</div>
		</div>`)

	targets := []tocTarget{
		{Fragment: "a", Title: "Section A", PlayOrder: 1},
		{Fragment: "b", Title: "Section B", PlayOrder: 2},
	}

	segments := splitBodyByTargets(body, targets)
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
}

func TestSplitBodyPreludeBeforeMultiTarget(t *testing.T) {
	body := parseBodyFromHTML(t, `
		<p>Frontmatter paragraph</p>
		<div>
			<h2 id="ch1">Ch1</h2>
			<p>text</p>
			<h2 id="ch2">Ch2</h2>
			<p>text</p>
		</div>`)

	targets := []tocTarget{
		{Fragment: "ch1", Title: "Ch1", PlayOrder: 1},
		{Fragment: "ch2", Title: "Ch2", PlayOrder: 2},
	}

	segments := splitBodyByTargets(body, targets)
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments (prelude + 2), got %d", len(segments))
	}
	if segments[0].ForceReason != "fragment:prelude" {
		t.Fatalf("expected prelude, got %q", segments[0].ForceReason)
	}
}

func TestSplitBodySelfTargetWrapper(t *testing.T) {
	body := parseBodyFromHTML(t, `
		<section id="part1">
			<h2>Part 1</h2>
			<p>Intro</p>
			<section id="ch1"><h3>Ch1</h3><p>C1</p></section>
			<section id="ch2"><h3>Ch2</h3><p>C2</p></section>
		</section>`)

	targets := []tocTarget{
		{Fragment: "part1", Title: "Part 1", PlayOrder: 1},
		{Fragment: "ch1", Title: "Ch 1", PlayOrder: 2},
		{Fragment: "ch2", Title: "Ch 2", PlayOrder: 3},
	}

	segments := splitBodyByTargets(body, targets)
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segments))
	}

	part1Text := collectSegmentText(segments[0].Nodes)
	if strings.Contains(part1Text, "C1") || strings.Contains(part1Text, "C2") {
		t.Fatalf("self-target segment should not contain child target content: %s", part1Text)
	}
}

func TestSplitBodySingleTargetUnchanged(t *testing.T) {
	body := parseBodyFromHTML(t, `
		<h1 id="intro">Introduction</h1>
		<p>Content</p>`)

	targets := []tocTarget{
		{Fragment: "intro", Title: "Introduction", PlayOrder: 1},
	}

	segments := splitBodyByTargets(body, targets)
	if len(segments) != 0 {
		t.Fatalf("single target should return nil, got %d", len(segments))
	}
}

func parseBodyFromHTML(t *testing.T, bodyContent string) *html.Node {
	t.Helper()

	doc, err := html.Parse(strings.NewReader("<html><body>" + bodyContent + "</body></html>"))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	body := findElement(doc, "body")
	if body == nil {
		t.Fatal("no body element")
	}
	return body
}

func collectSegmentText(nodes []*html.Node) string {
	var parts []string
	for _, node := range nodes {
		parts = append(parts, nodeText(node))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
