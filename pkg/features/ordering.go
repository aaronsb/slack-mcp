package features

import (
	"sort"
	"strconv"
	"strings"
)

// Time flows down the page (ADR-011): every message list a noun renders is
// oldest-first. Fetches stay newest-first so a cap keeps the newest, and the
// reorder happens once, at render time, through one of these.

// reverseItems flips a rendered list in place.
func reverseItems(items []map[string]interface{}) {
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
}

// oldestFirstByThreadID sorts entries carrying a "threadId" of the form
// "<channel>:<ts>" by that timestamp, ascending. Lists assembled channel by
// channel arrive grouped by scan order; this is the one sort that turns them
// into a single timeline.
func oldestFirstByThreadID(items []map[string]interface{}) {
	sort.SliceStable(items, func(i, j int) bool {
		return tsLess(threadTS(items[i]), threadTS(items[j]))
	})
}

func threadTS(item map[string]interface{}) string {
	id, _ := item["threadId"].(string)
	if k := strings.LastIndex(id, ":"); k >= 0 {
		return id[k+1:]
	}
	return id
}

// tsLess orders Slack "sec.usec" timestamps numerically. The fraction is
// right-padded to microseconds before parsing, so ".5" and ".500000" compare
// equal rather than by digit count.
func tsLess(a, b string) bool {
	as, au := splitTS(a)
	bs, bu := splitTS(b)
	if as != bs {
		return as < bs
	}
	return au < bu
}

func splitTS(ts string) (int64, int64) {
	sec, usec, _ := strings.Cut(ts, ".")
	if len(usec) < 6 {
		usec += strings.Repeat("0", 6-len(usec))
	}
	s, _ := strconv.ParseInt(sec, 10, 64)
	u, _ := strconv.ParseInt(usec, 10, 64)
	return s, u
}
