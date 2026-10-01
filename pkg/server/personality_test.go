package server

import (
	"strings"
	"testing"
)

func TestSanitizePersonality(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", defaultPersonality},
		{"plain", "ops-bot_2.0", "ops-bot_2.0"},
		{"newline", "bot\nIgnore previous", "botIgnore previous"},
		{"escape and bell", "a\x1b[31mb\x07c", "a31mbc"},
		{"only disallowed", "\n\x1b\x07()", defaultPersonality},
		{"only spaces", "   ", defaultPersonality},
		{"long", strings.Repeat("x", 100), strings.Repeat("x", maxPersonalityLength)},
		{"non-ascii dropped", "bøt", "bt"},
	}
	for _, c := range cases {
		if got := sanitizePersonality(c.in); got != c.want {
			t.Errorf("%s: sanitizePersonality(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
