package provider_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

// The provider must reach an overridden host with both of its clients — the
// slack-go one and the internal one — or nothing downstream is testable.
func TestProviderHonoursBaseURL(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)

	api, err := ap.Provide()
	if err != nil {
		t.Fatalf("Provide(): %v", err)
	}

	hist, err := api.GetConversationHistoryContext(context.Background(),
		&slack.GetConversationHistoryParameters{ChannelID: "C1", Limit: 10})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist.Messages) != 1 || hist.Messages[0].Text != "ship it" {
		t.Fatalf("history did not come from the fake: %+v", hist.Messages)
	}

	counts, err := ap.ProvideInternalClient().GetClientCounts(context.Background())
	if err != nil {
		t.Fatalf("client.counts: %v", err)
	}
	if !counts.OK || len(counts.Channels) != 1 {
		t.Fatalf("internal client did not come from the fake: %+v", counts)
	}
}

// Supplying a base URL skips team-endpoint discovery, so boot must not make a
// second auth.test round trip to rebuild the client.
func TestBaseURLSkipsEndpointDiscovery(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)

	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}

	// One call from captureIdentity. A second would mean boot rediscovered.
	if got := srv.Calls("auth.test"); got != 1 {
		t.Errorf("auth.test called %d times, want 1", got)
	}
}

// ADR-003 rests on client.counts returning `latest` for conversations that have
// no unreads. Pin that shape so a fixture drift is caught here rather than in
// the watermark logic.
func TestCountsCarryLatestWhenRead(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(
			[]any{
				slacktest.Conversation("C1", "1782246118.543969", "1782246118.543969", false, 0),
				slacktest.Conversation("C2", "1782200000.000000", "1782246999.000000", true, 1),
			},
			nil,
		)
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}

	counts, err := ap.ProvideInternalClient().GetClientCounts(context.Background())
	if err != nil {
		t.Fatalf("client.counts: %v", err)
	}
	if len(counts.Channels) != 2 {
		t.Fatalf("want 2 channels, got %d", len(counts.Channels))
	}

	read := counts.Channels[0]
	if read.HasUnreads {
		t.Fatal("fixture setup: first channel should be read")
	}
	if read.Latest == "" {
		t.Error("read conversation carries no `latest` — watermark comparison would be blind to it")
	}
}

// A read marker can sit ahead of the newest message. The watermark must never
// be derived from last_read, so pin that this state is representable.
func TestLastReadCanExceedLatest(t *testing.T) {
	srv := slacktest.New(t)
	srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(
			[]any{slacktest.Conversation("C1", "1773706775.693669", "1773706772.773579", false, 0)},
			nil,
		)
	})

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}

	counts, err := ap.ProvideInternalClient().GetClientCounts(context.Background())
	if err != nil {
		t.Fatalf("client.counts: %v", err)
	}
	c := counts.Channels[0]
	if c.LastRead <= c.Latest {
		t.Fatalf("fixture setup: want last_read ahead of latest, got %s vs %s", c.LastRead, c.Latest)
	}
}
