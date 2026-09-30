package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/cache"
	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/slack-go/slack"
)

// Shutdown must leave the writer election open for a successor (#82): an
// orphan holding either flock boots the next instance read-only.
func TestShutdownReleasesLedgersAndFlushes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const team, self = "T0SHUT", "U0SELF"

	st, err := estate.Open(team)
	if err != nil {
		t.Fatalf("open estate: %v", err)
	}
	attn, err := estate.OpenAttention(team, self, time.Now())
	if err != nil {
		t.Fatalf("open attention: %v", err)
	}
	if st.ReadOnly() || attn.ReadOnly() {
		t.Fatalf("first instance lost the writer election")
	}
	store, err := cache.NewStore()
	if err != nil {
		t.Fatalf("cache store: %v", err)
	}

	ap := NewWithTokens("xoxc-test", "xoxd-test")
	ap.store = store
	ap.estate, ap.attention = st, attn
	ap.users["U1"] = slack.User{ID: "U1", Name: "ada"}
	store.StartPeriodicFlush(time.Hour, ap.flushCaches)
	store.MarkDirty()

	ap.Shutdown()
	ap.Shutdown() // idempotent

	if ap.ctx.Err() == nil {
		t.Errorf("lifecycle context still live after Shutdown")
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), usersCacheFile)); err != nil {
		t.Errorf("dirty caches were not flushed on shutdown: %v", err)
	}

	st2, err := estate.Open(team)
	if err != nil {
		t.Fatalf("reopen estate: %v", err)
	}
	defer st2.Close()
	attn2, err := estate.OpenAttention(team, self, time.Now())
	if err != nil {
		t.Fatalf("reopen attention: %v", err)
	}
	defer attn2.Close()
	if st2.ReadOnly() {
		t.Errorf("successor booted the estate ledger read-only")
	}
	if attn2.ReadOnly() {
		t.Errorf("successor booted the attention ledger read-only")
	}
}

func TestShutdownOnAnUnbootedProvider(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	(&ApiProvider{}).Shutdown()
	NewWithTokens("xoxc-test", "xoxd-test").Shutdown()
}
