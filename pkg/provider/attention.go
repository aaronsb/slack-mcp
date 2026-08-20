package provider

import (
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
)

// The encounter observer (ADR-008 stage 2): the render paths already hold
// messages, so activity observation costs zero API calls. Each message
// yields at most one encounter — author, conversation, bucket — and the
// attention store's dedup collapses repeats, so re-reading a channel
// appends nothing new. No message text is ever passed down.

// EncounterSample is what a render path knows about one message: who wrote
// it and when. Text stays behind.
type EncounterSample struct {
	User string
	TS   string // Slack timestamp, "1755561600.123456"
}

// attn returns the attention store. Guarded like est(): assigned by the
// boot goroutine, read by handler goroutines.
func (ap *ApiProvider) attn() *estate.AttentionStore {
	ap.estateMu.RLock()
	defer ap.estateMu.RUnlock()
	return ap.attention
}

// openAttention opens this operator's attention ledger. Scope is the
// session's own user ID: two operators' agents see different traffic and
// must not mix ledgers. Failure degrades to nil, same as the estate.
func (ap *ApiProvider) openAttention() {
	if ap.selfTeamID == "" || ap.selfUserID == "" {
		log.Printf("Attention ledger disabled: no identity captured")
		return
	}
	att, err := estate.OpenAttention(ap.selfTeamID, ap.selfUserID, time.Now())
	if err != nil {
		log.Printf("Attention ledger unavailable: %v", err)
		return
	}
	ap.estateMu.Lock()
	ap.attention = att
	ap.estateMu.Unlock()
	if att.ReadOnly() {
		log.Printf("Attention ledger: read-only — another instance holds the writer lock")
	}
	s := att.Stats()
	log.Printf("Attention ledger: %d encounters, %d users, %d conversations (%s → %s)",
		s.Events, s.Users, s.Convs, s.FirstDay, s.LastDay)
}

// ObserveEncounters records that these messages' authors were active in
// conv. Best-effort: a nil or read-only store drops the observation, and an
// append error is logged, never surfaced — observation must not break the
// read that carried it.
func (ap *ApiProvider) ObserveEncounters(conv string, samples []EncounterSample) {
	att := ap.attn()
	if att == nil || att.ReadOnly() || conv == "" {
		return
	}

	encounters := make([]estate.Encounter, 0, len(samples))
	for _, s := range samples {
		if s.User == "" {
			continue
		}
		ts := slackTSTime(s.TS)
		if ts.IsZero() {
			continue
		}
		enc := estate.Encounter{User: s.User, Conv: conv, Day: ts.UTC().Format("2006-01-02")}
		if s.User == ap.selfUserID {
			h := ts.UTC().Hour()
			enc.Hour = &h
		}
		encounters = append(encounters, enc)
	}
	if len(encounters) == 0 {
		return
	}
	if _, err := att.Observe(encounters, time.Now()); err != nil {
		log.Printf("Attention observe: %v", err)
	}
}

// AttentionStats reports the activity plane's coverage gauge; ok is false
// when no attention ledger is open.
func (ap *ApiProvider) AttentionStats() (estate.AttentionStats, bool) {
	att := ap.attn()
	if att == nil {
		return estate.AttentionStats{}, false
	}
	return att.Stats(), true
}

// AttentionWritable reports whether this instance won the attention
// ledger's writer election — the gate for the compiled executor, whose
// results would otherwise be discarded.
func (ap *ApiProvider) AttentionWritable() bool {
	att := ap.attn()
	return att != nil && !att.ReadOnly()
}

// AttentionInfo is AttentionStats in one coverage-friendly value.
type AttentionInfo struct {
	Available bool
	// Writable is false when this instance lost the writer election —
	// its reads are not recorded, and views must say so.
	Writable bool
	Stats    estate.AttentionStats
}

func (ap *ApiProvider) AttentionInfo() AttentionInfo {
	st, ok := ap.AttentionStats()
	return AttentionInfo{Available: ok, Writable: ap.AttentionWritable(), Stats: st}
}

// slackTSTime parses a Slack "seconds.micros" timestamp; zero on failure.
func slackTSTime(ts string) time.Time {
	sec := ts
	if i := strings.IndexByte(ts, '.'); i >= 0 {
		sec = ts[:i]
	}
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}
