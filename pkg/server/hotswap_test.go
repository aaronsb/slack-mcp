package server

import (
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// Auth hot-swaps the provider in-process. flock is per open file, so an old
// provider still holding the ledgers wins the writer election and the new one
// boots read-only (#108). The swap must shut the old provider down first.
func TestSwapProviderShutsDownOldBeforeNewBoots(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	srv := slacktest.New(t)

	old := srv.Provider(t)
	if _, err := old.Provide(); err != nil {
		t.Fatalf("old Provide(): %v", err)
	}
	if !old.AttentionWritable() {
		t.Fatalf("old provider did not win the attention writer election")
	}

	s := NewSemanticMCPServer(old)
	fresh := srv.Provider(t)
	t.Cleanup(fresh.Shutdown)
	s.swapProvider(fresh)

	if s.provider.Load() != fresh {
		t.Fatalf("server is not serving the new provider")
	}

	deadline := time.Now().Add(5 * time.Second)
	for !fresh.AttentionWritable() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !fresh.AttentionWritable() {
		t.Errorf("new provider's attention ledger is read-only after hot-swap")
	}
	old.Shutdown() // idempotent: the swap already shut it down
}
