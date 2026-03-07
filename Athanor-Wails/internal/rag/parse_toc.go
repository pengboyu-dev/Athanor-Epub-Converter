package rag

import (
	"bytes"
	"path"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

type navPoint struct {
	Label struct {
		Text string `xml:"text"`
	} `xml:"navLabel"`
	Content struct {
		Src string `xml:"src,attr"`
	} `xml:"content"`
	Children []navPoint `xml:"navPoint"`
}

type ncxXML struct {
	NavMap struct {
		Points []navPoint `xml:"navPoint"`
	} `xml:"navMap"`
}

func extractTOCTargets(entries map[string]zipEntry, opfDir string, pkg packageXML) []tocTarget {
	var targets []tocTarget
	seen := map[string]struct{}{}
	for _, item := range pkg.Manifest.Items {
		full := resolveHref(opfDir, item.Href)
		if strings.Contains(item.Properties, "nav") {
			if entry, ok := entries[full]; ok {
				for _, target := range parseNavXHTML(entry.data, full) {
					key := target.HrefBase + "#" + target.Fragment + "|" + target.Title
					if _, exists := seen[key]; exists {
						continue
					}
					seen[key] = struct{}{}
					target.PlayOrder = len(targets) + 1
					targets = append(targets, target)
				}
			}
		}
		if strings.HasSuffix(strings.ToLower(item.Href), ".ncx") {
			if entry, ok := entries[full]; ok {
				for _, target := range parseNCX(entry.data, full) {
					key := target.HrefBase + "#" + target.Fragment + "|" + target.Title
					if _, exists := seen[key]; exists {
						continue
					}
					seen[key] = struct{}{}
					target.PlayOrder = len(targets) + 1
					targets = append(targets, target)
				}
			}
		}
	}
	return targets
}

func groupTOCTargetsByBase(targets []tocTarget) map[string][]tocTarget {
	grouped := map[string][]tocTarget{}
	for _, target := range targets {
		grouped[target.HrefBase] = append(grouped[target.HrefBase], target)
	}
	for href := range grouped {
		sort.SliceStable(grouped[href], func(i, j int) bool {
			return grouped[href][i].PlayOrder < grouped[href][j].PlayOrder
		})
	}
	return grouped
}

func parseNCX(data []byte, currentPath string) []tocTarget {
	var ncx ncxXML
	if err := decodeXML(data, &ncx); err != nil {
		return nil
	}

	var results []tocTarget
	var walk func(points []navPoint)
	walk = func(points []navPoint) {
		for _, point := range points {
			if point.Content.Src != "" {
				resolved := resolveHref(path.Dir(currentPath), point.Content.Src)
				results = append(results, tocTarget{
					HrefBase: strings.SplitN(resolved, "#", 2)[0],
					Fragment: fragmentID(point.Content.Src),
					Title:    strings.TrimSpace(point.Label.Text),
				})
			}
			walk(point.Children)
		}
	}
	walk(ncx.NavMap.Points)
	return results
}

func parseNavXHTML(data []byte, currentPath string) []tocTarget {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil
	}

	var results []tocTarget
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			href := attr(node, "href")
			text := strings.TrimSpace(nodeText(node))
			if href != "" && text != "" {
				resolved := resolveHref(path.Dir(currentPath), href)
				results = append(results, tocTarget{
					HrefBase: strings.SplitN(resolved, "#", 2)[0],
					Fragment: fragmentID(href),
					Title:    text,
				})
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return results
}
