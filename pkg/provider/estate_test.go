package provider_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
