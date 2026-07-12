package wecom

import "testing"

func TestStripWeComAtMentions(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		botIDs []string
		want   string
	}{
		{"no mention", "hello", []string{"bot1"}, "hello"},
		{"ascii mention", "@bot1 hello", []string{"bot1"}, "hello"},
		{"fullwidth mention", "＠bot1 hello", []string{"bot1"}, "hello"},
		{"multiple mentions", "@bot1 @bot1 hello", []string{"bot1"}, "hello"},
		{"case insensitive", "@BOT1 hello", []string{"bot1"}, "hello"},
		{"mention before slash command", "@bot1 /list", []string{"bot1"}, "/list"},
		{"mention with display name before slash", "@机器人 /list", []string{"bot1"}, "/list"},
		{"preserve url", "check https://example.com/a/b", []string{"bot1"}, "check https://example.com/a/b"},
		{"preserve path", "1/2 is a fraction", []string{"bot1"}, "1/2 is a fraction"},
		{"multiple bot ids", "@bot2 hello", []string{"bot1", "bot2"}, "hello"},
		{"empty after strip", "@bot1", []string{"bot1"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripWeComAtMentions(tc.input, tc.botIDs...)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
