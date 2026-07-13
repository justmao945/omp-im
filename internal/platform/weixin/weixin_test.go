package weixin

import (
	"testing"
)

func TestAccountLabel(t *testing.T) {
	cases := []struct {
		name string
		opts map[string]any
		want string
	}{
		{"name only", map[string]any{"name": "work"}, "work"},
		{"name overrides account_id", map[string]any{"name": "work", "account_id": "personal"}, "work"},
		{"account_id fallback", map[string]any{"account_id": "personal"}, "personal"},
		{"default", map[string]any{}, "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := accountLabel(c.opts); got != c.want {
				t.Errorf("accountLabel(%v) = %q, want %q", c.opts, got, c.want)
			}
		})
	}
}

func TestAllowList(t *testing.T) {
	cases := []struct {
		allow string
		user  string
		want  bool
	}{
		{"", "u", true},
		{"*", "u", true},
		{"u@im.wechat", "u@im.wechat", true},
		{"u@im.wechat", "v@im.wechat", false},
		{"u@im.wechat,v@im.wechat", "v@im.wechat", true},
		{"U@im.wechat", "u@im.wechat", true},
	}
	for _, c := range cases {
		got := allowList(c.allow, c.user)
		if got != c.want {
			t.Errorf("allowList(%q, %q) = %v, want %v", c.allow, c.user, got, c.want)
		}
	}
}

func TestBodyFromItemList(t *testing.T) {
	items := []messageItem{
		{Type: messageItemText, TextItem: &textItem{Text: "hello"}},
		{Type: messageItemText, TextItem: &textItem{Text: "world"}},
	}
	got := bodyFromItemList(items)
	want := "hello\nworld"
	if got != want {
		t.Errorf("bodyFromItemList = %q, want %q", got, want)
	}
}

func TestSplitUTF8(t *testing.T) {
	cases := []struct {
		s        string
		maxRunes int
		want     int
	}{
		{"hello", 10, 1},
		{"hello", 2, 3},
		{"", 5, 1},
		{"hi", 0, 1},
	}
	for _, c := range cases {
		got := splitUTF8(c.s, c.maxRunes)
		if len(got) != c.want {
			t.Errorf("splitUTF8(%q, %d) = %d chunks, want %d", c.s, c.maxRunes, len(got), c.want)
		}
	}
}

func TestSanitizePathSegment(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hello", "hello"},
		{"a/b", "a_b"},
		{":foo", "_foo"},
		{"", "default"},
	}
	for _, c := range cases {
		got := sanitizePathSegment(c.in)
		if got != c.want {
			t.Errorf("sanitizePathSegment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFooterEnabled(t *testing.T) {
	p, err := New(map[string]any{"token": "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.FooterEnabled() {
		t.Fatal("footer should default to enabled")
	}

	p2, err := New(map[string]any{"token": "test-token", "footer": false})
	if err != nil {
		t.Fatal(err)
	}
	if p2.FooterEnabled() {
		t.Fatal("footer should be disabled")
	}

	if _, err := New(map[string]any{"token": "test-token", "footer": "false"}); err == nil {
		t.Fatal("expected error for non-boolean footer")
	}
}
