package wecom

import (
	"strings"
	"testing"
	"time"
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

func TestBuildStreamContent(t *testing.T) {
	cases := []struct {
		name           string
		display        string
		finished       bool
		setup          func(*replyContext)
		wantContains   []string
		wantNotContain string
	}{
		{
			name:    "concise thinking",
			display: "concise",
			setup: func(rc *replyContext) {
				rc.thinkingText = "analyzing"
				rc.turnStart = time.Now().Add(-time.Second)
			},
			wantContains: []string{"thinking", "1s"},
		},
		{
			name:    "detailed thinking",
			display: "detailed",
			setup: func(rc *replyContext) {
				rc.thinkingText = "analyzing"
			},
			wantContains:   []string{"analyzing"},
			wantNotContain: "thinking",
		},
		{
			name:    "thinking off",
			display: "off",
			setup: func(rc *replyContext) {
				rc.thinkingText = "analyzing"
			},
			wantNotContain: "thinking",
		},
		{
			name:    "tool running",
			display: "concise",
			setup: func(rc *replyContext) {
				rc.toolName = "git status"
				rc.toolStart = time.Now().Add(-2 * time.Second)
			},
			wantContains: []string{"git status", "2s"},
		},
		{
			name:    "status hidden when body arrives",
			display: "concise",
			setup: func(rc *replyContext) {
				rc.thinkingText = "analyzing"
				rc.toolName = "git status"
				rc.streamText = "body text"
			},
			wantContains:   []string{"body text"},
			wantNotContain: "git status",
		},
		{
			name:     "finish footer",
			display:  "concise",
			finished: true,
			setup: func(rc *replyContext) {
				rc.turnStart = time.Now().Add(-10 * time.Second)
				rc.thinkingEnd = time.Now().Add(-8 * time.Second)
				rc.toolCount = 2
				rc.toolTotalDuration = 5 * time.Second
				rc.turnEnd = time.Now()
				rc.streamText = "result text"
			},
			wantContains: []string{
				"result text",
				"thinking",
				"2 tools",
				"5s",
				"total",
				"10s",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := &replyContext{}
			if tc.setup != nil {
				tc.setup(rc)
			}
			got := buildStreamContent(rc, tc.display, tc.finished)
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("content missing %q:\n%s", want, got)
				}
			}
			if tc.wantNotContain != "" && strings.Contains(got, tc.wantNotContain) {
				t.Fatalf("content should not contain %q:\n%s", tc.wantNotContain, got)
			}
		})
	}
}

func TestStreamFooter(t *testing.T) {
	rc := &replyContext{
		turnStart:         time.Now().Add(-10 * time.Second),
		thinkingEnd:       time.Now().Add(-8 * time.Second),
		toolCount:         2,
		toolTotalDuration: 5 * time.Second,
		turnEnd:           time.Now(),
	}
	footer := buildStreamFooter(rc)
	for _, want := range []string{"thinking", "2 tools", "5s", "total", "10s"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("footer missing %q:\n%s", want, footer)
		}
	}
}

