package estate_test

import (
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/slack-go/slack"
)

func TestChannelProjectionCarriesOwnership(t *testing.T) {
	var ch slack.Channel
	ch.ID, ch.Name = "C1", "acme-sales"
	ch.Creator = "U1"
	ch.Created = slack.JSONTime(1700000000)

	p := estate.ProjectChannel(ch)
	if p.Creator != "U1" || p.Created != 1700000000 {
		t.Fatalf("ownership dropped by the projection: %+v", p)
	}
}

func TestOwnershipArrivesAsAnHonestChange(t *testing.T) {
	st := open(t)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	var bare slack.Channel
	bare.ID, bare.Name = "C1", "acme-sales"
	if _, err := st.ObserveChannels([]slack.Channel{bare}, true, estate.SourceSweep, now); err != nil {
		t.Fatalf("observe: %v", err)
	}

	owned := bare
	owned.Creator = "U1"
	owned.Created = slack.JSONTime(1700000000)
	appended, err := st.ObserveChannels([]slack.Channel{owned}, true, estate.SourceSweep, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("observe with ownership: %v", err)
	}
	if appended.Appended != 1 {
		t.Fatalf("ownership arrival appended %d events, want 1", appended.Appended)
	}

	rec, ok := st.Channel("C1")
	if !ok {
		t.Fatalf("channel lost")
	}
	if rec.Props.Creator != "U1" || rec.Props.Created != 1700000000 {
		t.Fatalf("fold did not absorb ownership: %+v", rec.Props)
	}

	// Idempotence: the same observation again writes nothing.
	appended, err = st.ObserveChannels([]slack.Channel{owned}, true, estate.SourceSweep, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("re-observe: %v", err)
	}
	if appended.Appended != 0 {
		t.Fatalf("unchanged ownership appended %d events, want 0", appended.Appended)
	}
}
