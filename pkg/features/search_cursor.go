package features

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Slack's search.messages accepts count up to 100 and returns at most that
// many matches per page; further matches are reached by page number.
const (
	searchMaxCount     = 100
	searchDefaultCount = 100
)

// searchCursor is the continuation token for a search. It pins everything that
// decides which results a page holds: the Slack page, the page size, the window
// (so a search that widened keeps the window that produced the cursor), and a
// digest of the search, so a page 2 cannot silently run a different search than
// the page 1 that issued it.
type searchCursor struct {
	Page    int    `json:"p"`
	Count   int    `json:"c"`
	Since   string `json:"s"` // window start, YYYY-MM-DD
	Widened bool   `json:"w,omitempty"`
	Digest  string `json:"d"`
}

// searchDigest identifies a search independent of its window: the window
// travels in the cursor, so the digest covers what a caller means by "the same
// query" — the terms and the filters; the page size is pinned separately.
func searchDigest(query string, channels, people []string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%q|%q|%q", query, channels, people)))
	return hex.EncodeToString(sum[:8])
}

func (c searchCursor) encode() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeSearchCursor(s string) (searchCursor, error) {
	var c searchCursor
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return c, fmt.Errorf("not a search cursor")
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.Page < 1 || c.Count < 1 || c.Digest == "" {
		return c, fmt.Errorf("not a search cursor")
	}
	if _, err := time.Parse("2006-01-02", c.Since); err != nil {
		return c, fmt.Errorf("not a search cursor")
	}
	return c, nil
}

// explicitLimit reports whether the caller passed a limit, and its value
// clamped to Slack's bounds.
func explicitLimit(params map[string]interface{}) (int, bool) {
	var n int
	switch l := params["limit"].(type) {
	case float64:
		n = int(l)
	case int:
		n = l
	default:
		return 0, false
	}
	if n < 1 {
		return 0, false
	}
	if n > searchMaxCount {
		n = searchMaxCount
	}
	return n, true
}
