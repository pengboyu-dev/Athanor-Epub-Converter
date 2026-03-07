package rag

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
)

type evalQuery struct {
	Book           string   `json:"book"`
	Query          string   `json:"query"`
	ExpectedChunks []string `json:"expectedChunks"`
	TopK           int      `json:"topK,omitempty"`
}

func TestChunkRetrievalEval(t *testing.T) {
	if os.Getenv("ATHANOR_RUN_EVAL") == "" {
		t.Skip("set ATHANOR_RUN_EVAL=1 to run retrieval eval")
	}

	queries, err := loadEvalQueries(filepath.Join("testdata", "eval_queries.json"))
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("eval_queries.json not found")
		}
		t.Fatalf("load eval queries: %v", err)
	}
	if len(queries) == 0 {
		t.Skip("no eval queries configured")
	}

	chunksByBook := map[string][]Chunk{}
	hits := 0
	for _, query := range queries {
		if len(query.ExpectedChunks) == 0 {
			t.Fatalf("query %q has no expected chunks", query.Query)
		}
		if _, ok := chunksByBook[query.Book]; !ok {
			outputDir := testOutputDir(t, "eval-"+sanitizePathComponent(query.Book))
			input := filepath.Join("..", "..", "epub-raw", query.Book)
			result, err := ConvertEPUB(context.Background(), input, Options{
				OutputRootDir: outputDir,
				BaseName:      trimExtForEval(query.Book),
			})
			if err != nil {
				t.Fatalf("ConvertEPUB failed for %s: %v", query.Book, err)
			}
			chunksByBook[query.Book] = loadChunksForEval(t, result.ChunksPath)
		}

		k := query.TopK
		if k <= 0 {
			k = 5
		}
		got := retrieveTopK(chunksByBook[query.Book], query.Query, k)
		if overlapChunkIDs(got, query.ExpectedChunks) {
			hits++
			continue
		}
		t.Logf("MISS query=%q topK=%v expected=%v", query.Query, got, query.ExpectedChunks)
	}

	recall := float64(hits) / float64(len(queries))
	t.Logf("Recall@5=%.2f (%d/%d)", recall, hits, len(queries))
	if recall < 0.60 {
		t.Fatalf("recall below threshold: %.2f", recall)
	}
}

func loadEvalQueries(path string) ([]evalQuery, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var queries []evalQuery
	if err := json.Unmarshal(data, &queries); err != nil {
		return nil, err
	}
	return queries, nil
}

func loadChunksForEval(t *testing.T, path string) []Chunk {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read chunks: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	chunks := make([]Chunk, 0, len(lines))
	for _, line := range lines {
		var chunk Chunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatalf("unmarshal chunk: %v", err)
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func retrieveTopK(chunks []Chunk, query string, k int) []string {
	if k <= 0 {
		k = 5
	}
	queryTerms := tokenizeEvalText(query)
	if len(queryTerms) == 0 {
		return nil
	}

	type scored struct {
		id    string
		score float64
	}

	df := map[string]int{}
	docTerms := make([][]string, len(chunks))
	totalTerms := 0
	for i, chunk := range chunks {
		terms := tokenizeEvalText(chunk.Text)
		docTerms[i] = terms
		totalTerms += len(terms)
		seen := map[string]struct{}{}
		for _, term := range terms {
			if _, ok := seen[term]; ok {
				continue
			}
			seen[term] = struct{}{}
			df[term]++
		}
	}

	avgDL := 1.0
	if len(chunks) > 0 {
		avgDL = float64(totalTerms) / float64(len(chunks))
	}

	results := make([]scored, 0, len(chunks))
	n := float64(len(chunks))
	for i, chunk := range chunks {
		score := bm25Score(docTerms[i], queryTerms, df, n, avgDL)
		results = append(results, scored{id: chunk.ID, score: score})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].score == results[j].score {
			return results[i].id < results[j].id
		}
		return results[i].score > results[j].score
	})

	out := make([]string, 0, minEval(k, len(results)))
	for i := 0; i < k && i < len(results); i++ {
		out = append(out, results[i].id)
	}
	return out
}

func bm25Score(docTerms, queryTerms []string, df map[string]int, n, avgDL float64) float64 {
	if len(docTerms) == 0 || len(queryTerms) == 0 || n == 0 {
		return 0
	}
	const k1 = 1.2
	const b = 0.75

	tf := map[string]int{}
	for _, term := range docTerms {
		tf[term]++
	}

	score := 0.0
	docLen := float64(len(docTerms))
	for _, queryTerm := range queryTerms {
		freq := float64(tf[queryTerm])
		if freq == 0 {
			continue
		}
		dfVal := float64(df[queryTerm])
		idf := math.Log((n-dfVal+0.5)/(dfVal+0.5) + 1)
		tfNorm := (freq * (k1 + 1)) / (freq + k1*(1-b+b*docLen/avgDL))
		score += idf * tfNorm
	}
	return score
}

func tokenizeEvalText(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil
	}

	var tokens []string
	var ascii strings.Builder
	var cjkRunes []rune

	flushASCII := func() {
		if ascii.Len() == 0 {
			return
		}
		tokens = append(tokens, ascii.String())
		ascii.Reset()
	}
	flushCJK := func() {
		if len(cjkRunes) == 0 {
			return
		}
		for _, r := range cjkRunes {
			tokens = append(tokens, string(r))
		}
		for i := 0; i < len(cjkRunes)-1; i++ {
			tokens = append(tokens, string(cjkRunes[i:i+2]))
		}
		cjkRunes = cjkRunes[:0]
	}

	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if r <= unicode.MaxASCII {
				flushCJK()
				ascii.WriteRune(r)
			} else if isCJKRune(r) {
				flushASCII()
				cjkRunes = append(cjkRunes, r)
			} else {
				flushASCII()
				tokens = append(tokens, string(r))
			}
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return tokens
}

func overlapChunkIDs(a, b []string) bool {
	index := make(map[string]struct{}, len(b))
	for _, id := range b {
		index[id] = struct{}{}
	}
	for _, id := range a {
		if _, ok := index[id]; ok {
			return true
		}
	}
	return false
}

func trimExtForEval(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func minEval(a, b int) int {
	if a < b {
		return a
	}
	return b
}
