package provider

// Internal tests for the boot-walk skip: the decision must be checkable
// without racing backgroundBackfill's 10-second boot delay.

import (
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/slack-go/slack"
)

func seededEstate(t *testing.T, sweepAt time.Time) *estate.Store {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	st, err := estate.Open("T0SKIP")
	if err != nil {
		t.Fatalf("open estate: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	var c slack.Channel
	c.ID, c.Name = "C1", "eng"
	if _, err := st.ObserveChannels([]slack.Channel{c}, true, estate.SourceSweep, sweepAt); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if err := st.RecordSweep(estate.SweepReport{
		Channels: estate.ClassReport{Complete: true, Count: 1, ArchivedIncluded: true},
	}, sweepAt); err != nil {
		t.Fatalf("record sweep: %v", err)
	}
	return st
}

func TestAFreshEnumerationSkipsTheBootWalk(t *testing.T) {
	ap := &ApiProvider{estate: seededEstate(t, time.Now().Add(-time.Hour))}

	fresh, age := ap.channelEnumerationFresh()
	if !fresh {
		t.Fatalf("hour-old enumeration reported stale")
	}
	if age < 55*time.Minute || age > 65*time.Minute {
		t.Fatalf("age = %v, want about an hour", age)
	}

	// The skip path must complete synchronously — no client, no walk — and
	// mark the backfill done so the sweep scheduler proceeds.
	ap.backfillIfStale(t.Context())
	ap.backfillMutex.Lock()
	done := ap.backfillDone
	ap.backfillMutex.Unlock()
	if !done {
		t.Fatalf("skip did not mark the backfill done")
	}
}

func TestAStaleEnumerationDoesNotSkip(t *testing.T) {
	ap := &ApiProvider{estate: seededEstate(t, time.Now().Add(-25*time.Hour))}
	if fresh, _ := ap.channelEnumerationFresh(); fresh {
		t.Fatalf("day-old enumeration reported fresh against a 24h interval")
	}
}

func TestNoLedgerMeansTheWalkAlwaysRuns(t *testing.T) {
	ap := &ApiProvider{}
	if fresh, _ := ap.channelEnumerationFresh(); fresh {
		t.Fatalf("nil estate reported fresh")
	}
}
