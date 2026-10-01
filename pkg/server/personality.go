package server

import "strings"

const (
	defaultPersonality   = "slack-user"
	maxPersonalityLength = 32
)

// sanitizePersonality reduces SLACK_MCP_PERSONALITY to a short display
// label: letters, digits, space, '_', '.', '-', at most
// maxPersonalityLength runes, trimmed. Empty after that yields the default.
func sanitizePersonality(raw string) string {
	var b strings.Builder
	n := 0
	for _, r := range raw {
		if n >= maxPersonalityLength {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == ' ' || r == '_' || r == '.' || r == '-' {
			b.WriteRune(r)
			n++
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		return s
	}
	return defaultPersonality
}
