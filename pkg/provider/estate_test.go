package provider_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/aaronsb/slack-mcp/pkg/paths"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

// ledgerPath computes where the estate ledger for the fake workspace (team
// T1, per slacktest's auth.test fixture) lands under the test's XDG dir.
func ledgerPath() string {
	sum := sha256.Sum256([]byte("T1"))
	return filepath.Join(paths.DataDir(), "ledger", "T1-"+hex.EncodeToString(sum[:4]), "estate.jsonl")
}

type ledgerLine struct {
	Kind     string   `json:"kind"`
	Entity   string   `json:"entity"`
	ID       string   `json:"id"`
	Src      string   `json:"src"`
	Reason   string   `json:"reason"`
	Changed  []string `json:"changed"`
	Users    *struct{ Complete bool }
	Channels *struct{ Complete bool }
}

func readLedger(t *testing.T) []ledgerLine {
	t.Helper()
	data, err := os.ReadFile(ledgerPath())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var out []ledgerLine
	for _, raw := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if raw == "" {
			continue
		}
		var l ledgerLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("unparseable ledger line %q: %v", raw, err)
		}
		out = append(out, l)
	}
	return out
}

func find(lines []ledgerLine, kind, entity, id string) *ledgerLine {
	for i := range lines {
		if lines[i].Kind == kind && lines[i].Entity == entity && lines[i].ID == id {
			return &lines[i]
		}
	}
	return nil
}

func sweptProvider(t *testing.T) (*slacktest.Server, *provider.ApiProvider) {
	t.Helper()
	srv := slacktest.New(t)
	p := srv.Provider(t)
	if _, err := p.Provide(); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	srv.Quiesce(t)
	return srv, p
}

func TestASweepSeedsTheLedgerWithTheDirectory(t *testing.T) {
	srv, p := sweptProvider(t)
	_ = srv

	if err := p.RunEstateSweep(context.Background()); err != nil {
		t.Fatalf("RunEstateSweep: %v", err)
	}

	lines := readLedger(t)
	for _, id := range []string{"U1", "U2"} {
		if find(lines, "first-seen", "user", id) == nil {
			t.Fatalf("no first-seen for user %s in %+v", id, lines)
		}
	}
	for _, id := range []string{"C1", "C2"} {
		if find(lines, "first-seen", "channel", id) == nil {
			t.Fatalf("no first-seen for channel %s", id)
		}
	}

	var sweep *ledgerLine
	for i := range lines {
		if lines[i].Kind == "sweep" {
			sweep = &lines[i]
		}
	}
	if sweep == nil {
		t.Fatalf("no sweep event")
	}
	if sweep.Users == nil || !sweep.Users.Complete || sweep.Channels == nil || !sweep.Channels.Complete {
		t.Fatalf("sweep event does not report both classes complete: %+v", sweep)
	}
}

func TestAnUnchangedWorkspaceAppendsOnlyTheSweepEvent(t *testing.T) {
	_, p := sweptProvider(t)

	if err := p.RunEstateSweep(context.Background()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	before := len(readLedger(t))

	if err := p.RunEstateSweep(context.Background()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	after := readLedger(t)
	if len(after) != before+1 {
		t.Fatalf("second sweep appended %d lines, want exactly the sweep event", len(after)-before)
	}
	if after[len(after)-1].Kind != "sweep" {
		t.Fatalf("trailing line is %q, want sweep", after[len(after)-1].Kind)
	}
}

func TestASweepRecordsARenameAsAChange(t *testing.T) {
	srv, p := sweptProvider(t)

	if err := p.RunEstateSweep(context.Background()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}

	srv.SeedUsers(
		slack.User{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"},
		slack.User{ID: "U2", Name: "schen", RealName: "Sarah Chen-Okafor"},
	)
	if err := p.RunEstateSweep(context.Background()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}

	change := find(readLedger(t), "changed", "user", "U2")
	if change == nil {
		t.Fatalf("rename produced no changed event")
	}
	if len(change.Changed) != 1 || change.Changed[0] != "realName" {
		t.Fatalf("changed = %v, want [realName]", change.Changed)
	}
	if change.Src != "sweep" {
		t.Fatalf("src = %q, want sweep", change.Src)
	}
}

func TestASweepTombstonesADepartureAndRevivesAReturn(t *testing.T) {
	srv, p := sweptProvider(t)
	ctx := context.Background()

	if err := p.RunEstateSweep(ctx); err != nil {
		t.Fatalf("seed sweep: %v", err)
	}

	srv.SeedUsers(slack.User{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"})
	if err := p.RunEstateSweep(ctx); err != nil {
		t.Fatalf("departure sweep: %v", err)
	}
	stone := find(readLedger(t), "tombstone", "user", "U2")
	if stone == nil {
		t.Fatalf("departed user was not tombstoned")
	}
	if stone.Reason != "absent" {
		t.Fatalf("reason = %q, want absent", stone.Reason)
	}

	srv.SeedUsers(
		slack.User{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"},
		slack.User{ID: "U2", Name: "schen", RealName: "Sarah Chen"},
	)
	if err := p.RunEstateSweep(ctx); err != nil {
		t.Fatalf("revival sweep: %v", err)
	}
	lines := readLedger(t)
	var revived bool
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Kind == "first-seen" && lines[i].ID == "U2" {
			revived = true
			break
		}
		if lines[i].Kind == "tombstone" && lines[i].ID == "U2" {
			break
		}
	}
	if !revived {
		t.Fatalf("returned user was not revived with a fresh first-seen")
	}
}

func TestADeactivationIsTombstonedAsAPositiveFact(t *testing.T) {
	srv, p := sweptProvider(t)
	ctx := context.Background()

	if err := p.RunEstateSweep(ctx); err != nil {
		t.Fatalf("seed sweep: %v", err)
	}

	srv.SeedUsers(
		slack.User{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"},
		slack.User{ID: "U2", Name: "schen", RealName: "Sarah Chen", Deleted: true},
	)
	if err := p.RunEstateSweep(ctx); err != nil {
		t.Fatalf("deactivation sweep: %v", err)
	}

	stone := find(readLedger(t), "tombstone", "user", "U2")
	if stone == nil || stone.Reason != "deactivated" {
		t.Fatalf("want a deactivated tombstone, got %+v", stone)
	}
}

func TestASweepCostsOneCallPerEnumeration(t *testing.T) {
	srv, p := sweptProvider(t)

	srv.ResetCalls()
	if err := p.RunEstateSweep(context.Background()); err != nil {
		t.Fatalf("RunEstateSweep: %v", err)
	}

	for method, want := range map[string]int{
		"users.list":          1,
		"conversations.list":  1,
		"users.conversations": 1,
	} {
		if got := srv.Calls(method); got != want {
			t.Fatalf("%s called %d times, want %d", method, got, want)
		}
	}
}

func TestAProviderWithoutALedgerStillServes(t *testing.T) {
	srv := slacktest.New(t)

	// Damage the ledger's interior before boot: estate.Open must refuse it,
	// and the provider must degrade to ledger-less operation, not fail.
	dir := filepath.Dir(ledgerPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	damaged := "not json at all\n" +
		`{"v":1,"at":"2026-08-18T12:00:00Z","src":"sweep","kind":"sweep","users":{"complete":true}}` + "\n"
	if err := os.WriteFile(ledgerPath(), []byte(damaged), 0o600); err != nil {
		t.Fatalf("write damaged ledger: %v", err)
	}

	p := srv.Provider(t)
	if _, err := p.Provide(); err != nil {
		t.Fatalf("Provide should degrade, got: %v", err)
	}
	srv.Quiesce(t)

	if got := len(p.ProvideUsersMap()); got == 0 {
		t.Fatalf("degraded provider serves no users")
	}
	if err := p.RunEstateSweep(context.Background()); err == nil {
		t.Fatalf("sweep without a ledger should error")
	}
}

// seedLedger writes estate history for team T1 before the provider boots,
// standing in for what earlier sessions observed.
func seedLedger(t *testing.T, observe func(*estate.Store)) {
	t.Helper()
	st, err := estate.Open("T1")
	if err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	observe(st)
	if err := st.Close(); err != nil {
		t.Fatalf("close seeded ledger: %v", err)
	}
}

var estateBase = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

func TestATombstonedUserLoadsDeletedFromTheSnapshot(t *testing.T) {
	srv := slacktest.New(t)

	seedLedger(t, func(st *estate.Store) {
		both := []slack.User{
			{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"},
			{ID: "U2", Name: "schen", RealName: "Sarah Chen"},
		}
		if _, err := st.ObserveUsers(both, true, estate.SourceSweep, estateBase); err != nil {
			t.Fatalf("observe: %v", err)
		}
		if _, err := st.ObserveUsers(both[:1], true, estate.SourceSweep, estateBase.Add(24*time.Hour)); err != nil {
			t.Fatalf("tombstone: %v", err)
		}
	})

	p := srv.Provider(t)
	if _, err := p.Provide(); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	srv.Quiesce(t)

	// The snapshot still contains U2 alive; the fold's tombstone wins.
	u2, ok := p.ProvideUsersMap()["U2"]
	if !ok {
		t.Fatalf("tombstoned user missing from the map entirely — historical resolution needs it")
	}
	if !u2.Deleted {
		t.Fatalf("tombstoned user loaded alive: %+v", u2)
	}
}

func TestAFoldLiveUserMissingFromTheSnapshotHydrates(t *testing.T) {
	srv := slacktest.New(t)

	seedLedger(t, func(st *estate.Store) {
		ghost := slack.User{ID: "U9", Name: "ghost", RealName: "Gil Host"}
		if _, err := st.ObserveUsers([]slack.User{ghost}, false, estate.SourceTraffic, estateBase); err != nil {
			t.Fatalf("observe: %v", err)
		}
	})

	p := srv.Provider(t)
	if _, err := p.Provide(); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	srv.Quiesce(t)

	u9, ok := p.ProvideUsersMap()["U9"]
	if !ok {
		t.Fatalf("fold-live user not hydrated from the estate")
	}
	if u9.Name != "ghost" || u9.RealName != "Gil Host" {
		t.Fatalf("skeleton lost its projection: %+v", u9)
	}
}

func TestAGoneChannelStaysOutOfTheLiveMap(t *testing.T) {
	srv := slacktest.New(t)

	seedLedger(t, func(st *estate.Store) {
		var c1, c2 slack.Channel
		c1.ID, c1.Name = "C1", "eng"
		c2.ID, c2.Name = "C2", "platform"
		if _, err := st.ObserveChannels([]slack.Channel{c1, c2}, true, estate.SourceSweep, estateBase); err != nil {
			t.Fatalf("observe: %v", err)
		}
		if _, err := st.ObserveChannels([]slack.Channel{c1}, true, estate.SourceSweep, estateBase.Add(24*time.Hour)); err != nil {
			t.Fatalf("tombstone: %v", err)
		}
	})

	// The live fixtures no longer list C2; the stale snapshot still does.
	var c1 slack.Channel
	c1.ID, c1.Name = "C1", "eng"
	c1.IsMember = true
	srv.SeedChannels(c1)
	p := srv.Provider(t)

	var c2 slack.Channel
	c2.ID, c2.Name = "C2", "platform"
	staleSnapshot, err := json.Marshal([]slack.Channel{c1, c2})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.DataDir(), "channels.json"), staleSnapshot, 0o600); err != nil {
		t.Fatalf("write stale snapshot: %v", err)
	}

	if _, err := p.Provide(); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	srv.Quiesce(t)

	for _, ch := range p.GetCachedChannels() {
		if ch.ID == "C2" {
			t.Fatalf("gone channel loaded into the live map")
		}
	}
	rec, ok := p.EstateChannels()["C2"]
	if !ok || rec.Gone == nil {
		t.Fatalf("gone channel lost its dated absence: %+v", rec)
	}
	if !rec.Gone.At.Equal(estateBase.Add(24 * time.Hour)) {
		t.Fatalf("tombstone date %v, want %v", rec.Gone.At, estateBase.Add(24*time.Hour))
	}
}

func TestADeletedSnapshotSelfHealsFromTheEstate(t *testing.T) {
	srv := slacktest.New(t)

	seedLedger(t, func(st *estate.Store) {
		users := []slack.User{
			{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"},
			{ID: "U2", Name: "schen", RealName: "Sarah Chen"},
		}
		if _, err := st.ObserveUsers(users, true, estate.SourceSweep, estateBase); err != nil {
			t.Fatalf("observe: %v", err)
		}
	})

	p := srv.Provider(t)
	if err := os.Remove(filepath.Join(paths.DataDir(), "users.json")); err != nil {
		t.Fatalf("remove snapshot: %v", err)
	}

	if _, err := p.Provide(); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	srv.Quiesce(t)

	users := p.ProvideUsersMap()
	if len(users) != 2 {
		t.Fatalf("hydrated %d users, want 2", len(users))
	}
	// Hydration fills the boot gate, so the lost snapshot triggers no
	// synchronous directory fetch — the sweep owns freshness now.
	if got := srv.Calls("users.list"); got != 0 {
		t.Fatalf("users.list called %d times at boot, want 0", got)
	}
}

func TestASecondSweepSkipsTheFreshChannelWalk(t *testing.T) {
	srv, p := sweptProvider(t)

	if err := p.RunEstateSweep(context.Background()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}

	srv.ResetCalls()
	if err := p.RunEstateSweep(context.Background()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}

	// The channel enumeration is fresh, so the second sweep re-walks
	// neither conversations.list nor the membership walk — only users.
	if got := srv.Calls("conversations.list"); got != 0 {
		t.Fatalf("second sweep walked channels %d times, want 0", got)
	}
	if got := srv.Calls("users.conversations"); got != 0 {
		t.Fatalf("second sweep walked membership %d times, want 0", got)
	}
	if got := srv.Calls("users.list"); got != 1 {
		t.Fatalf("second sweep listed users %d times, want 1", got)
	}
}

func walkStatePath() string {
	return filepath.Join(filepath.Dir(ledgerPath()), "walk-state.json")
}

// A reconnect can kill the server mid-walk at any time. The walk must
// resume from its checkpoint, not restart from page one — and the absence
// pass must run against the union of resumed and fresh pages.
func TestAnInterruptedWalkResumesFromItsCheckpoint(t *testing.T) {
	srv := slacktest.New(t)

	// The interrupted walk had already seen C1 (page-wise observation in
	// the ledger) and checkpointed cursor CUR2.
	seedLedger(t, func(st *estate.Store) {
		var c1 slack.Channel
		c1.ID, c1.Name = "C1", "eng"
		if _, err := st.ObserveChannels([]slack.Channel{c1}, false, estate.SourceSweep, estateBase); err != nil {
			t.Fatalf("observe: %v", err)
		}
	})
	state := fmt.Sprintf(`{"cursor":"CUR2","startedAt":%q,"seen":["C1"]}`,
		time.Now().Add(-time.Minute).Format(time.RFC3339))
	if err := os.WriteFile(walkStatePath(), []byte(state), 0o600); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	var gotCursors []string
	var mu sync.Mutex
	srv.Handle("conversations.list", func(r *http.Request) any {
		_ = r.ParseForm()
		cursor := r.Form.Get("cursor")
		mu.Lock()
		gotCursors = append(gotCursors, cursor)
		mu.Unlock()
		if cursor == "CUR2" {
			var c2 slack.Channel
			c2.ID, c2.Name = "C2", "platform"
			return map[string]any{
				"ok":                true,
				"channels":          []slack.Channel{c2},
				"response_metadata": map[string]any{"next_cursor": ""},
			}
		}
		return map[string]any{
			"ok": false, "error": "test should not walk from page one",
		}
	})

	p := srv.Provider(t)
	if _, err := p.Provide(); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	srv.Quiesce(t)
	if err := p.RunEstateSweep(context.Background()); err != nil {
		t.Fatalf("RunEstateSweep: %v", err)
	}

	mu.Lock()
	cursors := append([]string(nil), gotCursors...)
	mu.Unlock()
	if len(cursors) != 1 || cursors[0] != "CUR2" {
		t.Fatalf("walk did not resume from the checkpoint: cursors %v", cursors)
	}

	// C1 was not re-fetched, yet it is in the resumed seen set — the
	// absence pass must not tombstone it.
	if rec, ok := p.EstateChannels()["C1"]; !ok || rec.Gone != nil {
		t.Fatalf("resumed-seen channel was tombstoned: %+v", rec)
	}
	if rec, ok := p.EstateChannels()["C2"]; !ok || rec.Gone != nil {
		t.Fatalf("fresh page not observed: %+v", rec)
	}
	if _, err := os.Stat(walkStatePath()); !os.IsNotExist(err) {
		t.Fatalf("completed walk left its checkpoint behind")
	}
}

func TestACompletedWalkLeavesNoCheckpoint(t *testing.T) {
	_, p := sweptProvider(t)
	if err := p.RunEstateSweep(context.Background()); err != nil {
		t.Fatalf("RunEstateSweep: %v", err)
	}
	if _, err := os.Stat(walkStatePath()); !os.IsNotExist(err) {
		t.Fatalf("completed walk left a checkpoint")
	}
}
