package wecom

import (
	"strings"
	"testing"
	"time"

	"github.com/justmao945/omp-im/internal/core"
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
		level          string
		finished       bool
		setup          func(*replyContext)
		wantContains   []string
		wantNotContain string
	}{
		{
			name:  "concise thinking",
			level: "concise",
			setup: func(rc *replyContext) {
				rc.thinkingText = "analyzing"
				rc.turnStart = time.Now().Add(-time.Second)
			},
			wantContains: []string{"thinking", "1s"},
		},
		{
			name:  "detailed thinking",
			level: "detailed",
			setup: func(rc *replyContext) {
				rc.thinkingText = "analyzing"
			},
			wantContains:   []string{"analyzing"},
			wantNotContain: "thinking",
		},
		{
			name:  "thinking off",
			level: "off",
			setup: func(rc *replyContext) {
				rc.thinkingText = "analyzing"
			},
			wantNotContain: "thinking",
		},
		{
			name:  "tool running",
			level: "concise",
			setup: func(rc *replyContext) {
				rc.toolName = "git status"
				rc.toolStart = time.Now().Add(-2 * time.Second)
			},
			wantContains: []string{"git status", "2s"},
		},
		{
			name:  "tool result flash",
			level: "concise",
			setup: func(rc *replyContext) {
				rc.toolResult = "done"
			},
			wantContains: []string{"done"},
		},
		{
			name:     "finish summary",
			level:    "concise",
			finished: true,
			setup: func(rc *replyContext) {
				rc.toolCount = 2
				rc.toolTotalDuration = 5 * time.Second
				rc.streamText = "result text"
			},
			wantContains: []string{"result text", "used 2 tools", "5s"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := &replyContext{}
			if tc.setup != nil {
				tc.setup(rc)
			}
			got := buildStreamContent(rc, tc.level, tc.finished)
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

func TestStreamEventUpdatesState(t *testing.T) {
	rc := &replyContext{}
	updateStreamEvent(rc, core.StreamEvent{Type: "thinking", Text: "step 1"})
	if !strings.Contains(rc.thinkingText, "step 1") {
		t.Fatalf("thinking text not accumulated")
	}

	updateStreamEvent(rc, core.StreamEvent{Type: "tool_start", Tool: "ls"})
	if rc.toolName != "ls" {
		t.Fatalf("toolName = %q", rc.toolName)
	}

	updateStreamEvent(rc, core.StreamEvent{Type: "tool_end", Result: "ok"})
	if rc.toolCount != 1 {
		t.Fatalf("toolCount = %d", rc.toolCount)
	}
	if rc.toolResult != "ok" {
		t.Fatalf("toolResult = %q", rc.toolResult)
	}
	if rc.toolName != "" {
		t.Fatalf("toolName should be cleared")
	}
}

func updateStreamEvent(rc *replyContext, ev core.StreamEvent) {
	switch ev.Type {
	case "thinking":
		rc.thinkingText += ev.Text
	case "tool_start":
		rc.toolName = ev.Tool
		rc.toolStart = time.Now()
	case "tool_end":
		if !rc.toolStart.IsZero() {
			rc.toolTotalDuration += time.Since(rc.toolStart)
		}
		rc.toolCount++
		rc.toolResult = ev.Result
		if rc.toolResult == "" {
			rc.toolResult = "done"
		}
		rc.toolName = ""
		rc.toolStart = time.Time{}
	}
}

