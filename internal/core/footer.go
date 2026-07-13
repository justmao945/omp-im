// FooterInfo carries the data needed to build a turn-summary footer
// (⏱️ Xs · 🧠 X% · 🛠️ N) appended to agent replies. It is shared across
// platforms for both streaming and non-streaming reply paths.
package core

import (
	"fmt"
	"strings"
	"time"
)

type FooterInfo struct {
	Duration    time.Duration
	ContextUsed int
	ContextSize int
	ToolCount   int
	ShowTools   bool
}

// BuildFooter assembles the footer string from the given info.
// Returns an empty string if Duration is zero.
func BuildFooter(info FooterInfo) string {
	if info.Duration <= 0 {
		return ""
	}
	var items []string
	items = append(items, fmt.Sprintf("⏱️ %s", FormatDuration(info.Duration)))
	if info.ContextSize > 0 {
		pct := info.ContextUsed * 100 / info.ContextSize
		items = append(items, fmt.Sprintf("🧠 %d%%", pct))
	}
	if info.ShowTools && info.ToolCount > 0 {
		items = append(items, fmt.Sprintf("🛠️ %d", info.ToolCount))
	}
	return strings.Join(items, " · ")
}

// FormatDuration returns a human-readable duration rounded to seconds.
// Values under a minute are shown as "Xs"; otherwise "Xm" or "Xm Ys".
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	secs := int(d.Seconds())
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	secs = secs % 60
	if secs == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm%ds", mins, secs)
}
