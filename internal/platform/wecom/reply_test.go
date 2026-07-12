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
		thinking       string
		tool           string
		finished       bool
		setup          func(*replyContext)
		wantContains   []string
		wantNotContain string
	}{
		{
			name: "processing before thinking",
			setup: func(rc *replyContext) {
				rc.turnStart = time.Now().Add(-3 * time.Second)
			},
			wantContains: []string{"Processing", "3s"},
		},
		{
			name:     "processing hidden when body arrives",
			thinking: "concise",
			tool:     "concise",
			setup: func(rc *replyContext) {
				rc.turnStart = time.Now().Add(-3 * time.Second)
				rc.streamText = "body text"
			},
			wantContains:   []string{"body text"},
			wantNotContain: "处理中",
		},
		{
			name:     "concise thinking",
			thinking: "concise",
			setup: func(rc *replyContext) {
				rc.thinkingText = "analyzing"
				rc.turnStart = time.Now().Add(-time.Second)
			},
			wantContains: []string{"Thinking...", "1s"},
		},
		{
			name:     "detailed thinking",
			thinking: "detailed",
			setup: func(rc *replyContext) {
				rc.thinkingText = "analyzing"
			},
			wantContains:   []string{"analyzing"},
			wantNotContain: "Thinking...",
		},
		{
			name:     "thinking off",
			thinking: "off",
			setup: func(rc *replyContext) {
				rc.thinkingText = "analyzing"
			},
			wantNotContain: "Thinking...",
		},
		{
			name: "tool running concise",
			tool: "concise",
			setup: func(rc *replyContext) {
				rc.toolName = "git status"
				rc.toolStart = time.Now().Add(-2 * time.Second)
			},
			wantContains: []string{"git status", "2s"},
		},
		{
			name: "tool running detailed",
			tool: "detailed",
			setup: func(rc *replyContext) {
				rc.toolName = "ls"
				rc.toolStart = time.Now().Add(-2 * time.Second)
				rc.toolHistory = []toolRecord{{
					name:  "ls",
					input: "{\"path\":\"/tmp\"}",
					start: time.Now().Add(-2 * time.Second),
				}}
			},
			wantContains: []string{"ls", "2s", "path"},
		},
		{
			name: "tool off",
			tool: "off",
			setup: func(rc *replyContext) {
				rc.toolName = "git status"
			},
			wantNotContain: "git status",
		},
		{
			name:     "status hidden when body arrives",
			thinking: "concise",
			tool:     "concise",
			setup: func(rc *replyContext) {
				rc.thinkingText = "analyzing"
				rc.toolName = "git status"
				rc.streamText = "body text"
			},
			wantContains:   []string{"body text"},
			wantNotContain: "git status",
		},
		{
			name:     "finish footer with context",
			thinking: "concise",
			tool:     "concise",
			finished: true,
			setup: func(rc *replyContext) {
				rc.turnStart = time.Now().Add(-10 * time.Second)
				rc.thinkingEnd = time.Now().Add(-8 * time.Second)
				rc.toolCount = 2
				rc.toolTotalDuration = 5 * time.Second
				rc.turnEnd = time.Now()
				rc.streamText = "result text"
				rc.contextUsed = 53000
				rc.contextSize = 200000
			},
			wantContains: []string{
				"result text",
				"thinking",
				"2 tools",
				"5s",
				"total",
				"10s",
				"context usage 26%",
			},
		},
		{
			name:     "finish footer",
			thinking: "concise",
			tool:     "concise",
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
		{
			name:     "detailed tool footer summary only",
			thinking: "off",
			tool:     "detailed",
			finished: true,
			setup: func(rc *replyContext) {
				rc.turnStart = time.Now().Add(-10 * time.Second)
				rc.toolCount = 1
				rc.toolTotalDuration = 3 * time.Second
				rc.turnEnd = time.Now()
				rc.streamText = "result"
				rc.toolHistory = []toolRecord{{
					name:   "cat",
					input:  "{\"path\":\"/etc/passwd\"}",
					output: "root:x:0:0",
					start:  time.Now().Add(-5 * time.Second),
					end:    time.Now().Add(-2 * time.Second),
				}}
			},
			wantContains:   []string{"result", "1 tool", "3s"},
			wantNotContain: "root:x",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := &replyContext{}
			if tc.setup != nil {
				tc.setup(rc)
			}
			got := buildStreamContent(rc, tc.thinking, tc.tool, tc.finished)
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
	cases := []struct {
		name           string
		thinking       string
		tool           string
		setup          func(*replyContext)
		wantContains   []string
		wantNotContain string
	}{
		{
			name:     "thinking and tools",
			thinking: "concise",
			tool:     "concise",
			setup: func(rc *replyContext) {
				rc.turnStart = time.Now().Add(-10 * time.Second)
				rc.thinkingEnd = time.Now().Add(-8 * time.Second)
				rc.toolCount = 2
				rc.toolTotalDuration = 5 * time.Second
				rc.turnEnd = time.Now()
			},
			wantContains: []string{"thinking", "2 tools", "5s", "total", "10s"},
		},
		{
			name:     "tool off hides tool summary",
			thinking: "concise",
			tool:     "off",
			setup: func(rc *replyContext) {
				rc.turnStart = time.Now().Add(-10 * time.Second)
				rc.thinkingEnd = time.Now().Add(-8 * time.Second)
				rc.toolCount = 2
				rc.toolTotalDuration = 5 * time.Second
				rc.turnEnd = time.Now()
			},
			wantContains:   []string{"thinking"},
			wantNotContain: "tools",
		},
		{
			name:     "detailed tool footer summary only",
			thinking: "off",
			tool:     "detailed",
			setup: func(rc *replyContext) {
				rc.turnStart = time.Now().Add(-10 * time.Second)
				rc.toolCount = 1
				rc.toolTotalDuration = 3 * time.Second
				rc.turnEnd = time.Now()
				rc.toolHistory = []toolRecord{{
					name:   "cat",
					input:  "{\"path\":\"/etc/passwd\"}",
					output: "root:x:0:0",
					start:  time.Now().Add(-5 * time.Second),
					end:    time.Now().Add(-2 * time.Second),
				}}
			},
			wantContains:   []string{"1 tool", "3s", "total", "10s"},
			wantNotContain: "root:x",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := &replyContext{}
			if tc.setup != nil {
				tc.setup(rc)
			}
			footer := buildStreamFooter(rc, tc.thinking, tc.tool)
			for _, want := range tc.wantContains {
				if !strings.Contains(footer, want) {
					t.Fatalf("footer missing %q:\n%s", want, footer)
				}
			}
			if tc.wantNotContain != "" && strings.Contains(footer, tc.wantNotContain) {
				t.Fatalf("footer should not contain %q:\n%s", tc.wantNotContain, footer)
			}
		})
	}
}

func TestStreamEventUpdatesState(t *testing.T) {
	rc := &replyContext{}
	updateStreamEvent(rc, core.StreamEvent{Type: "processing"})
	if rc.turnStart.IsZero() {
		t.Fatalf("processing event should set turnStart")
	}

	updateStreamEvent(rc, core.StreamEvent{Type: "thinking", Text: "step 1"})
	if !strings.Contains(rc.thinkingText, "step 1") {
		t.Fatalf("thinking text not accumulated")
	}

	updateStreamEvent(rc, core.StreamEvent{Type: "tool_start", Tool: "ls", ToolInput: "{\"path\":\"/tmp\"}"})
	if rc.toolName != "ls" {
		t.Fatalf("toolName = %q", rc.toolName)
	}
	if len(rc.toolHistory) != 1 {
		t.Fatalf("toolHistory len = %d", len(rc.toolHistory))
	}
	if rc.toolHistory[0].input != "{\"path\":\"/tmp\"}" {
		t.Fatalf("toolHistory input = %q", rc.toolHistory[0].input)
	}

	updateStreamEvent(rc, core.StreamEvent{Type: "tool_end", ToolOutput: "ok"})
	if rc.toolCount != 1 {
		t.Fatalf("toolCount = %d", rc.toolCount)
	}
	if rc.toolHistory[0].output != "ok" {
		t.Fatalf("toolHistory output = %q", rc.toolHistory[0].output)
	}
	if rc.toolName != "" {
		t.Fatalf("toolName should be cleared")
	}
}

func updateStreamEvent(rc *replyContext, ev core.StreamEvent) {
	switch ev.Type {
	case "processing":
		if rc.turnStart.IsZero() {
			rc.turnStart = time.Now()
		}
	case "thinking":
		rc.thinkingText += ev.Text
	case "tool_start":
		if rc.thinkingText != "" && rc.thinkingEnd.IsZero() {
			rc.thinkingEnd = time.Now()
		}
		rc.toolName = ev.Tool
		rc.toolStart = time.Now()
		rc.toolHistory = append(rc.toolHistory, toolRecord{
			name:  ev.Tool,
			input: ev.ToolInput,
			start: time.Now(),
		})
	case "tool_end":
		if !rc.toolStart.IsZero() {
			rc.toolTotalDuration += time.Since(rc.toolStart)
		}
		rc.toolCount++
		rc.toolName = ""
		rc.toolStart = time.Time{}
		if n := len(rc.toolHistory); n > 0 {
			rc.toolHistory[n-1].output = ev.ToolOutput
			rc.toolHistory[n-1].end = time.Now()
		}
	}
}
