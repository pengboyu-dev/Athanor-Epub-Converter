package rag

import "testing"

func TestJoinInlineParts(t *testing.T) {
	tests := []struct {
		name     string
		parts    []string
		expected string
	}{
		{
			name:     "cjk adjacent nodes",
			parts:    []string{"\u5b57", "\u4f53"},
			expected: "\u5b57\u4f53",
		},
		{
			name:     "footnote marker without leading space",
			parts:    []string{"\u6b63\u6587", "[^1]"},
			expected: "\u6b63\u6587[^1]",
		},
		{
			name:     "english words keep separator",
			parts:    []string{"hello", "world"},
			expected: "hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinInlineParts(tt.parts); got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestShouldMergeParagraph(t *testing.T) {
	tests := []struct {
		name     string
		prev     string
		next     string
		expected bool
	}{
		{
			name:     "cjk paragraph stays split",
			prev:     "\u8fd9\u662f\u4e2d\u6587\u6bb5\u843d",
			next:     "\u8fd9\u662f\u4e0b\u4e00\u6bb5",
			expected: false,
		},
		{
			name:     "hyphenated english line merges",
			prev:     "inter-",
			next:     "national",
			expected: true,
		},
		{
			name:     "sentence boundary blocks merge",
			prev:     "Hello.",
			next:     "world",
			expected: false,
		},
		{
			name:     "lowercase english continuation merges",
			prev:     "continued",
			next:     "line",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldMergeParagraph(tt.prev, tt.next); got != tt.expected {
				t.Fatalf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestNormalizeParagraphV2(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "remove spaces between cjk runes",
			input:    "\u4e2d \u6587 \u5b57 \u7b26",
			expected: "\u4e2d\u6587\u5b57\u7b26",
		},
		{
			name:     "keep mixed zh en spacing",
			input:    "\u4e2d\u6587 English \u6df7\u6392",
			expected: "\u4e2d\u6587 English \u6df7\u6392",
		},
		{
			name:     "trim cjk punctuation spaces",
			input:    "\u4e2d\u6587 \uff0c \u6807\u70b9",
			expected: "\u4e2d\u6587\uff0c\u6807\u70b9",
		},
		{
			name:     "drop zero width chars",
			input:    "\u4e2d\u200b\u6587",
			expected: "\u4e2d\u6587",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeParagraphV2(tt.input); got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}
