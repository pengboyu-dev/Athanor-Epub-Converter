package rag

import (
	"strings"

	"golang.org/x/net/html"
)

type chapterSegment struct {
	Title       string
	Nodes       []*html.Node
	ForceKind   ChapterKind
	ForceReason string
	Warnings    []string
}

func splitBodyByTargets(body *html.Node, targets []tocTarget) []chapterSegment {
	if len(targets) <= 1 {
		return nil
	}

	hasFragments := false
	for _, target := range targets {
		if target.Fragment != "" {
			hasFragments = true
			break
		}
	}
	if !hasFragments {
		return nil
	}

	segments, _ := splitSiblingsByTargets(bodyChildren(body), targets, 0)
	out := make([]chapterSegment, 0, len(segments))
	for _, segment := range segments {
		if len(segment.Nodes) == 0 {
			continue
		}
		out = append(out, segment)
	}
	return out
}

func splitSiblingsByTargets(siblings []*html.Node, targets []tocTarget, start int) ([]chapterSegment, int) {
	var segments []chapterSegment
	next := start

	for _, node := range siblings {
		if isIgnorableSplitNode(node) {
			continue
		}
		if next >= len(targets) {
			if len(segments) > 0 {
				segments[len(segments)-1].Nodes = append(segments[len(segments)-1].Nodes, node)
			}
			continue
		}

		matchFirst := firstMatchingTargetIndex(node, targets, next)
		if matchFirst < 0 {
			if len(segments) == 0 {
				segments = append(segments, chapterSegment{
					Title:       "Prefatory Pages",
					ForceKind:   ChapterKindFrontMatter,
					ForceReason: "fragment:prelude",
				})
			}
			segments[len(segments)-1].Nodes = append(segments[len(segments)-1].Nodes, node)
			continue
		}

		matchCount := matchingTargetCount(node, targets, next)
		if matchCount <= 1 {
			segments = append(segments, chapterSegment{
				Title: targets[matchFirst].Title,
				Nodes: []*html.Node{node},
			})
			next = matchFirst + 1
			continue
		}

		children := bodyChildren(node)
		if len(children) == 0 {
			segments = append(segments, chapterSegment{
				Title:    targets[matchFirst].Title,
				Nodes:    []*html.Node{node},
				Warnings: []string{"splitter:multi_target_unsplittable"},
			})
			next = lastMatchingTargetIndex(node, targets, next) + 1
			continue
		}

		selfIdx := selfTargetIndex(node, targets, next)
		if selfIdx >= 0 {
			subSegments, subNext := splitSiblingsByTargets(children, targets, selfIdx+1)

			selfNodes := []*html.Node(nil)
			if len(subSegments) > 0 && subSegments[0].ForceReason == "fragment:prelude" {
				selfNodes = append(selfNodes, subSegments[0].Nodes...)
				subSegments = subSegments[1:]
			}
			if len(selfNodes) == 0 && len(subSegments) == 0 {
				segments = append(segments, chapterSegment{
					Title: targets[selfIdx].Title,
					Nodes: []*html.Node{node},
				})
				next = subNext
				continue
			}
			if len(selfNodes) > 0 {
				segments = append(segments, chapterSegment{
					Title: targets[selfIdx].Title,
					Nodes: selfNodes,
				})
			}
			segments = append(segments, subSegments...)
			next = subNext
			continue
		}

		subSegments, subNext := splitSiblingsByTargets(children, targets, next)
		if len(subSegments) <= 1 {
			segments = append(segments, chapterSegment{
				Title:    targets[matchFirst].Title,
				Nodes:    []*html.Node{node},
				Warnings: []string{"splitter:multi_target_no_split"},
			})
			next = lastMatchingTargetIndex(node, targets, next) + 1
			continue
		}

		mergeStart := 0
		if subSegments[0].ForceReason == "fragment:prelude" {
			if len(segments) > 0 {
				segments[len(segments)-1].Nodes = append(segments[len(segments)-1].Nodes, subSegments[0].Nodes...)
			} else {
				segments = append(segments, subSegments[0])
			}
			mergeStart = 1
		}
		segments = append(segments, subSegments[mergeStart:]...)
		next = subNext
	}

	return segments, next
}

func firstMatchingTargetIndex(node *html.Node, targets []tocTarget, start int) int {
	for i := start; i < len(targets); i++ {
		if targets[i].Fragment == "" {
			continue
		}
		if subtreeHasID(node, targets[i].Fragment) {
			return i
		}
	}
	return -1
}

func matchingTargetCount(node *html.Node, targets []tocTarget, start int) int {
	count := 0
	for i := start; i < len(targets); i++ {
		if targets[i].Fragment == "" {
			continue
		}
		if subtreeHasID(node, targets[i].Fragment) {
			count++
		}
	}
	return count
}

func selfTargetIndex(node *html.Node, targets []tocTarget, start int) int {
	if node == nil || node.Type != html.ElementNode {
		return -1
	}
	id := attr(node, "id")
	if id == "" {
		return -1
	}
	for i := start; i < len(targets); i++ {
		if targets[i].Fragment == id {
			return i
		}
	}
	return -1
}

func lastMatchingTargetIndex(node *html.Node, targets []tocTarget, start int) int {
	last := start
	for i := start; i < len(targets); i++ {
		if targets[i].Fragment == "" {
			continue
		}
		if subtreeHasID(node, targets[i].Fragment) {
			last = i
		}
	}
	return last
}

func subtreeHasID(node *html.Node, id string) bool {
	if node == nil || id == "" {
		return false
	}
	if node.Type == html.ElementNode && attr(node, "id") == id {
		return true
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if subtreeHasID(child, id) {
			return true
		}
	}
	return false
}

func bodyChildren(body *html.Node) []*html.Node {
	var nodes []*html.Node
	for child := body.FirstChild; child != nil; child = child.NextSibling {
		nodes = append(nodes, child)
	}
	return nodes
}

func isIgnorableSplitNode(node *html.Node) bool {
	return node != nil && node.Type == html.TextNode && strings.TrimSpace(node.Data) == ""
}
