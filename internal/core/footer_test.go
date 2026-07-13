package core

import (
	"testing"
	"time"
)

func TestBuildFooter(t *testing.T) {
	cases := []struct {
		name string
		info FooterInfo
		want string
	}{
		{
			name: "duration only",
			info: FooterInfo{Duration: 10 * time.Second},
			want: "⏱️ 10s",
		},
		{
			name: "duration and context",
			info: FooterInfo{Duration: 10 * time.Second, ContextUsed: 53000, ContextSize: 200000},
			want: "⏱️ 10s · 🧠 26%",
		},
		{
			name: "duration context tools",
			info: FooterInfo{Duration: 10 * time.Second, ContextUsed: 53000, ContextSize: 200000, ToolCount: 3, ShowTools: true},
			want: "⏱️ 10s · 🧠 26% · 🛠️ 3",
		},
		{
			name: "tools hidden when ShowTools false",
			info: FooterInfo{Duration: 5 * time.Second, ToolCount: 3, ShowTools: false},
			want: "⏱️ 5s",
		},
		{
			name: "zero duration returns empty",
			info: FooterInfo{Duration: 0, ContextSize: 200000},
			want: "",
		},
		{
			name: "minute formatting",
			info: FooterInfo{Duration: 90 * time.Second},
			want: "⏱️ 1m30s",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildFooter(tc.info)
			if got != tc.want {
				t.Fatalf("BuildFooter = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{300 * time.Millisecond, "0s"},
		{3 * time.Second, "3s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{90 * time.Second, "1m30s"},
		{-5 * time.Second, "0s"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := FormatDuration(tc.d)
			if got != tc.want {
				t.Fatalf("FormatDuration(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}
