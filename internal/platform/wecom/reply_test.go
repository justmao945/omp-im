package wecom

import (
	"strings"
	"testing"
)

func TestSplitTextChunks(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		maxBytes int
		want     []string
	}{
		{"empty", "", 10, nil},
		{"short ascii", "hello", 10, []string{"hello"}},
		{"split ascii", "hello world", 5, []string{"hello", " worl", "d"}},
		{"chinese rune not split", "你好世界", 6, []string{"你好", "世界"}},
		{"chinese single byte", "你好世界", 1, []string{"你", "好", "世", "界"}},
		{"single rune larger than max", "你", 1, []string{"你"}},
		{"long repeated", strings.Repeat("a", 10), 3, []string{"aaa", "aaa", "aaa", "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitTextChunks(tc.text, tc.maxBytes)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("chunk %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
