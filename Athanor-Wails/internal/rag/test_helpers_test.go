package rag

import (
	"os"
	"path/filepath"
	"testing"
)

func testOutputDir(t *testing.T, name string) string {
	t.Helper()

	dir := filepath.Join("..", "..", ".tmp", "rag-tests", name)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir test output dir: %v", err)
	}
	return dir
}
