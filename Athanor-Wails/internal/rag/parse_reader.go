package rag

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
)

type containerXML struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

type guideRefXML struct {
	Type  string `xml:"type,attr"`
	Title string `xml:"title,attr"`
	Href  string `xml:"href,attr"`
}

type packageXML struct {
	Metadata struct {
		Title      []string `xml:"title"`
		Creator    []string `xml:"creator"`
		Language   []string `xml:"language"`
		Publisher  []string `xml:"publisher"`
		Date       []string `xml:"date"`
		Identifier []string `xml:"identifier"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		Itemrefs []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
	Guide struct {
		Refs []guideRefXML `xml:"reference"`
	} `xml:"guide"`
}

type zipEntry struct {
	name string
	data []byte
}

type tocTarget struct {
	HrefBase  string
	Fragment  string
	Title     string
	PlayOrder int
}

type manifestItem struct {
	Href       string
	Properties string
}

func openEPUBEntries(inputPath string) (*zip.ReadCloser, map[string]zipEntry, error) {
	reader, err := zip.OpenReader(inputPath)
	if err != nil {
		return nil, nil, fmt.Errorf("打开 EPUB 失败: %w", err)
	}

	entries := map[string]zipEntry{}
	for _, file := range reader.File {
		rc, err := file.Open()
		if err != nil {
			reader.Close()
			return nil, nil, fmt.Errorf("读取 EPUB 条目失败: %w", err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			reader.Close()
			return nil, nil, fmt.Errorf("读取 EPUB 条目失败: %w", err)
		}
		entries[file.Name] = zipEntry{name: file.Name, data: data}
	}
	return reader, entries, nil
}

func loadPackageDocument(entries map[string]zipEntry) (string, packageXML, error) {
	containerData, ok := entries["META-INF/container.xml"]
	if !ok {
		return "", packageXML{}, fmt.Errorf("缺少 META-INF/container.xml")
	}

	var container containerXML
	if err := decodeXML(containerData.data, &container); err != nil {
		return "", packageXML{}, fmt.Errorf("解析 container.xml 失败: %w", err)
	}
	if len(container.Rootfiles) == 0 {
		return "", packageXML{}, fmt.Errorf("EPUB 缺少 rootfile")
	}

	opfPath := container.Rootfiles[0].FullPath
	opfEntry, ok := entries[opfPath]
	if !ok {
		return "", packageXML{}, fmt.Errorf("找不到 OPF: %s", opfPath)
	}

	var pkg packageXML
	if err := decodeXML(opfEntry.data, &pkg); err != nil {
		return "", packageXML{}, fmt.Errorf("解析 OPF 失败: %w", err)
	}
	return opfPath, pkg, nil
}

func buildManifestIndex(opfDir string, pkg packageXML) map[string]manifestItem {
	manifest := make(map[string]manifestItem, len(pkg.Manifest.Items))
	for _, item := range pkg.Manifest.Items {
		manifest[item.ID] = manifestItem{
			Href:       resolveHref(opfDir, item.Href),
			Properties: item.Properties,
		}
	}
	return manifest
}

func metadataFromPackage(pkg packageXML) Metadata {
	return Metadata{
		Title:         firstNonEmpty(pkg.Metadata.Title...),
		Authors:       filterNonEmpty(pkg.Metadata.Creator),
		Language:      firstNonEmpty(pkg.Metadata.Language...),
		Publisher:     firstNonEmpty(pkg.Metadata.Publisher...),
		PublishedDate: firstNonEmpty(pkg.Metadata.Date...),
		Identifier:    firstNonEmpty(pkg.Metadata.Identifier...),
	}
}

func decodeXML(data []byte, out any) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	return decoder.Decode(out)
}

func resolveHref(baseDir, href string) string {
	if href == "" {
		return ""
	}
	trimmed := strings.SplitN(href, "#", 2)[0]
	if trimmed == "" {
		return ""
	}
	if baseDir == "." || baseDir == "/" {
		return path.Clean(trimmed)
	}
	return path.Clean(path.Join(baseDir, trimmed))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func filterNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
