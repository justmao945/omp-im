package acp

import (
	"strings"
	"testing"

	"github.com/justmao945/omp-im/internal/core"
)

func TestBuildPromptWithFiles(t *testing.T) {
	cases := []struct {
		name  string
		files []core.FileAttachment
		want  string
	}{
		{
			name:  "text file",
			files: []core.FileAttachment{{FileName: "hello.txt", MimeType: "text/plain", Data: []byte("world")}},
			want:  "prompt\n\n[attached file: hello.txt (text/plain)]\nworld",
		},
		{
			name:  "binary file",
			files: []core.FileAttachment{{FileName: "doc.pdf", MimeType: "application/pdf", Data: []byte{0x25, 0x50, 0x44, 0x46}}},
			want:  "prompt\n\n[attached file: doc.pdf (application/pdf)]",
		},
		{
			name:  "json file",
			files: []core.FileAttachment{{FileName: "data.json", MimeType: "application/json", Data: []byte(`{"a":1}`)}},
			want:  "prompt\n\n[attached file: data.json (application/json)]\n{\"a\":1}",
		},
		{
			name:  "truncated text file",
			files: []core.FileAttachment{{FileName: "big.txt", MimeType: "text/plain", Data: []byte(strings.Repeat("a", 60000))}},
			want:  "prompt\n\n[attached file: big.txt (text/plain)]\n" + strings.Repeat("a", 50000) + "\n... (truncated)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildPromptWithFiles("prompt", tc.files)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildPromptWithFilesEmpty(t *testing.T) {
	got := buildPromptWithFiles("hello", nil)
	if got != "hello" {
		t.Fatalf("got %q", got)
	}
}
