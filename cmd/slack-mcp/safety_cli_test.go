package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/safety"
)

type fakeDir struct {
	targets map[string]cliTarget
	names   map[string]string
}

func (f *fakeDir) Org() safety.Org { return safety.Org{TeamID: "T1", UserID: "UME"} }
func (f *fakeDir) Resolve(arg string) (cliTarget, error) {
	if t, ok := f.targets[arg]; ok {
		return t, nil
	}
	return cliTarget{}, fmt.Errorf("%s: not found", arg)
}
func (f *fakeDir) CurrentName(k safety.Key) (string, bool) {
	n, ok := f.names[k.ID]
	return n, ok
}

type cliHarness struct {
	t         *testing.T
	ws        *safety.Workspace
	dir       string
	env       map[string]string
	tty       bool
	connected bool
	out, err  bytes.Buffer
}

func newHarness(t *testing.T) *cliHarness {
	return &cliHarness{t: t, dir: t.TempDir(), env: map[string]string{}, tty: true}
}

func (h *cliHarness) workspace(p safety.Posture) *safety.Workspace {
	w, err := safety.OpenDir(h.dir, safety.Org{TeamID: "T1", UserID: "UME"}, p)
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

func (h *cliHarness) run(stdin string, args ...string) int {
	h.out.Reset()
	h.err.Reset()
	fd := &fakeDir{
		targets: map[string]cliTarget{
			"#partner": {Key: safety.Conversation("C1", "#partner"), DestKind: safety.DestChannel, External: true, Parties: []string{"TACME"}},
			"#general": {Key: safety.Conversation("C2", "#general"), DestKind: safety.DestChannel},
			"@dana":    {Key: safety.Person("U1", "@dana"), DestKind: safety.DestPerson},
		},
		names: map[string]string{"C1": "#partner-renamed"},
	}
	c := &safetyCLI{
		stdin:      strings.NewReader(stdin),
		stdout:     &h.out,
		stderr:     &h.err,
		isTerminal: func() bool { return h.tty },
		lookupEnv:  func(k string) (string, bool) { v, ok := h.env[k]; return v, ok },
		now:        time.Now,
		connect: func() (directory, error) {
			h.connected = true
			return fd, nil
		},
		open: func(org safety.Org, p safety.Posture) (*safety.Workspace, error) {
			if org.TeamID != "T1" {
				h.t.Fatalf("workspace keyed by %q, want the auth.test team ID", org.TeamID)
			}
			return safety.OpenDir(h.dir, org, p)
		},
	}
	return c.run(args)
}

func blockOn(t *testing.T, w *safety.Workspace, d safety.Destination) {
	t.Helper()
	if _, err := w.Quarantine.RecordBlock(safety.Block{Destination: d, Match: safety.MatchRecord{Class: "jwt", Location: "text"}}); err != nil {
		t.Fatal(err)
	}
}

func TestSafetyCLIRefusesWithoutTerminal(t *testing.T) {
	for _, args := range [][]string{
		{"approve", "p7k2"},
		{"deny", "p7k2"},
		{"quarantine", "clear", "#general"},
		{"quarantine", "clear", "strikes"},
		{"trust", "add", "#partner"},
	} {
		h := newHarness(t)
		h.tty = false
		w := h.workspace(safety.Strict)
		blockOn(t, w, safety.Destination{Kind: safety.DestChannel, ConversationID: "C2", Name: "#general"})
		code := h.run(strings.Join(args[len(args)-1:], "")+"\n", args...)
		if code == 0 || !strings.Contains(h.err.String(), "interactive terminal") {
			t.Fatalf("%v: code %d, stderr %q", args, code, h.err.String())
		}
		if h.connected {
			t.Fatalf("%v: reached Slack before refusing", args)
		}
		if st := w.Quarantine.State(); st.Strikes != 1 || len(st.Conversations) != 1 {
			t.Fatalf("%v changed state", args)
		}
		if w.Trust.State().Entries(safety.Conversation("C1", "")) {
			t.Fatalf("%v added trust", args)
		}
	}
}

func TestSafetyCLIQuarantineClearConfirmation(t *testing.T) {
	h := newHarness(t)
	w := h.workspace(safety.Strict)
	blockOn(t, w, safety.Destination{Kind: safety.DestChannel, ConversationID: "C2", Name: "#general"})

	if code := h.run("#generl\n", "quarantine", "clear", "#general"); code == 0 {
		t.Fatal("mismatched confirmation accepted")
	}
	if !strings.Contains(h.err.String(), "did not match") {
		t.Fatalf("stderr %q", h.err.String())
	}
	if _, q := w.Quarantine.State().IsQuarantined("C2"); !q {
		t.Fatal("mismatch cleared the quarantine")
	}
	if code := h.run("", "quarantine", "clear", "#general"); code == 0 {
		t.Fatal("empty confirmation accepted")
	}

	if code := h.run("#general\n", "quarantine", "clear", "general"); code != 0 {
		t.Fatalf("clear: %d %s", code, h.err.String())
	}
	if _, q := w.Quarantine.State().IsQuarantined("C2"); q {
		t.Fatal("not cleared")
	}
	if code := h.run("#general\n", "quarantine", "clear", "#general"); code == 0 {
		t.Fatal("clearing an open destination succeeded")
	}
}

func TestSafetyCLIQuarantineClearStrikesAndTrustNote(t *testing.T) {
	h := newHarness(t)
	w := h.workspace(safety.Strict)
	w.Trust.Add(safety.TrustAdd{Key: safety.Conversation("C2", "#general"), Cases: []safety.Case{safety.CaseCrossConversation}, Source: safety.SourceCLI})
	blockOn(t, w, safety.Destination{Kind: safety.DestChannel, ConversationID: "C2", Name: "#general"})
	blockOn(t, w, safety.Destination{Kind: safety.DestChannel, ConversationID: "C3", Name: "#x"})

	h.run("", "quarantine", "list")
	if !strings.Contains(h.out.String(), "Strike lock engaged (2 of 2)") {
		t.Fatalf("list: %s", h.out.String())
	}
	if code := h.run("strikes\n", "quarantine", "clear", "strikes"); code != 0 {
		t.Fatalf("clear strikes: %s", h.err.String())
	}
	if w.Quarantine.State().LockEngaged() {
		t.Fatal("lock still engaged")
	}
	h.run("#general\n", "quarantine", "clear", "#general")
	if !strings.Contains(h.out.String(), "trusted destination") {
		t.Fatalf("clear of a trusted destination does not say so: %s", h.out.String())
	}
}

func TestSafetyCLIApproveAndDeny(t *testing.T) {
	h := newHarness(t)
	w := h.workspace(safety.Strict)
	now := time.Now()
	r, _, err := w.Pending.Create(safety.Request{
		Cases: []safety.Case{safety.CaseExternal}, Tool: "say",
		Destination: safety.Destination{Kind: safety.DestChannel, ConversationID: "C1", Name: "#partner"},
		ContentHash: "h", Text: "the full text to send", FileNames: []string{"q3.xlsx"}, FileCount: 1,
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	// Listing needs no terminal.
	h.tty = false
	if code := h.run("", "approve"); code != 0 || !strings.Contains(h.out.String(), r.ID) {
		t.Fatalf("list: %d %s", code, h.out.String())
	}
	h.tty = true

	if code := h.run("pzzz\n", "approve", r.ID); code == 0 {
		t.Fatal("mismatched ID approved")
	}
	if !strings.Contains(h.out.String(), "the full text to send") || !strings.Contains(h.out.String(), "q3.xlsx") {
		t.Fatalf("case 1 content not shown: %s", h.out.String())
	}
	if got, _ := w.Pending.Lookup(r.ID, now); got.Status != safety.StatusPending {
		t.Fatal("mismatch changed the request")
	}
	if code := h.run(r.ID+"\n", "approve", r.ID); code != 0 {
		t.Fatalf("approve: %s", h.err.String())
	}
	if got, _ := w.Pending.Lookup(r.ID, now); got.Status != safety.StatusApproved {
		t.Fatalf("status %s", got.Status)
	}

	r2, _, _ := w.Pending.Create(safety.Request{Cases: []safety.Case{safety.CaseCrossConversation}, Tool: "say",
		Destination: safety.Destination{Kind: safety.DestChannel, ConversationID: "C2", Name: "#general"}, ContentHash: "h2"}, now)
	if code := h.run(r2.ID+"\n", "deny", r2.ID); code != 0 {
		t.Fatalf("deny: %s", h.err.String())
	}
	if got, _ := w.Pending.Lookup(r2.ID, now); got.Status != safety.StatusDenied {
		t.Fatalf("status %s", got.Status)
	}
	if code := h.run("pnone\n", "approve", "pnone"); code == 0 {
		t.Fatal("unknown ID approved")
	}
}

// Approving a lift whose key was cleared after it was issued (here on the
// page) and closed again lifts nothing for that key, and the CLI says so
// instead of "lifted".
func TestSafetyCLIApproveStaleLiftSaysNothingLifted(t *testing.T) {
	h := newHarness(t)
	w := h.workspace(safety.Soft)
	general := safety.Destination{Kind: safety.DestChannel, ConversationID: "C2", Name: "#general"}
	blockOn(t, w, general)
	blockOn(t, w, general) // soft quarantines from the second block
	now := time.Now()
	r, _, err := w.IssueLift(safety.Request{Cases: []safety.Case{safety.CaseLift}, Tool: "say",
		Destination: general, Lift: []safety.Key{safety.Conversation("C2", "#general")}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Quarantine.Clear(safety.Conversation("C2", "#general"), safety.ByWeb, "", now); err != nil {
		t.Fatal(err)
	}
	if code := h.run(r.ID+"\n", "approve", r.ID); code != 0 {
		t.Fatalf("approve: %s", h.err.String())
	}
	out := h.out.String()
	if !strings.Contains(out, "#general") || !strings.Contains(out, "was cleared after this request was issued; nothing lifted for it") || strings.Contains(out, ": lifted.") {
		t.Fatalf("output: %s", out)
	}
}

// A strikes lift is also how a server holding writes after an unrecorded
// block is released, and approving it releases that hold even when no
// recorded strike was lifted; the output must not say nothing happened.
func TestSafetyCLIApproveSkippedStrikesLiftNamesTheHold(t *testing.T) {
	h := newHarness(t)
	w := h.workspace(safety.Strict)
	now := time.Now()
	blockOn(t, w, safety.Destination{Kind: safety.DestChannel, ConversationID: "C2", Name: "#general"})
	r, _, err := w.IssueLift(safety.Request{Cases: []safety.Case{safety.CaseLift}, Tool: "say", Lift: []safety.Key{safety.StrikesKey}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Quarantine.Clear(safety.StrikesKey, safety.ByWeb, "", now); err != nil {
		t.Fatal(err)
	}
	if code := h.run(r.ID+"\n", "approve", r.ID); code != 0 {
		t.Fatalf("approve: %s", h.err.String())
	}
	if out := h.out.String(); !strings.Contains(out, "releases that hold") {
		t.Fatalf("output: %s", out)
	}
}

func TestSafetyCLITrustAddRemoveList(t *testing.T) {
	h := newHarness(t)
	w := h.workspace(safety.Strict)

	if code := h.run("#wrong\n", "trust", "add", "#partner", "--case", "external"); code == 0 {
		t.Fatal("mismatch added trust")
	}
	if w.Trust.State().Entries(safety.Conversation("C1", "")) {
		t.Fatal("mismatch wrote an entry")
	}
	if code := h.run("#partner\n", "trust", "add", "#partner", "--case=external", "--for", "30d"); code != 0 {
		t.Fatalf("add: %s", h.err.String())
	}
	if !strings.Contains(h.out.String(), "TACME") {
		t.Fatalf("external parties not shown: %s", h.out.String())
	}
	e, ok := w.Trust.State().Trusted(safety.TrustQuery{
		Destination: safety.Destination{Kind: safety.DestChannel, ConversationID: "C1"},
		Case:        safety.CaseExternal, Parties: []string{"TACME"}, PartiesKnown: true,
	}, time.Now())
	if !ok || e.Expires == nil || e.Source != safety.SourceCLI {
		t.Fatalf("entry: %+v %v", e, ok)
	}

	// An elicitation entry from a soft session, listed as ignored in strict.
	soft := h.workspace(safety.Soft)
	soft.Trust.Add(safety.TrustAdd{Key: safety.Conversation("C2", "#general"), Cases: []safety.Case{safety.CaseExternal}, Source: safety.SourceElicitation, PendingID: "pabc"})

	h.run("", "trust", "list")
	out := h.out.String()
	for _, want := range []string{"Posture: strict (default)", "#partner-renamed", "[in effect]", "#general (name at add time)", "[ignored in strict]", "pabc"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list lacks %q:\n%s", want, out)
		}
	}
	h.env[safety.SafetyEnv] = "soft"
	h.run("", "trust", "list")
	if !strings.Contains(h.out.String(), "Posture: soft (SLACK_MCP_SAFETY)") || strings.Contains(h.out.String(), "ignored") {
		t.Fatalf("soft list:\n%s", h.out.String())
	}

	// Remove needs no terminal and no confirmation.
	h.tty = false
	if code := h.run("", "trust", "remove", "#partner"); code != 0 {
		t.Fatalf("remove: %s", h.err.String())
	}
	if w.Trust.State().Entries(safety.Conversation("C1", "")) {
		t.Fatal("not removed")
	}
	if code := h.run("", "trust", "remove", "#partner"); code == 0 {
		t.Fatal("second remove succeeded")
	}
}

func TestSafetyCLIRefusesUnknownPosture(t *testing.T) {
	h := newHarness(t)
	h.env[safety.SafetyEnv] = "lenient"
	if code := h.run("", "quarantine", "list"); code == 0 || !strings.Contains(h.err.String(), "want strict or soft") {
		t.Fatalf("code %d stderr %q", code, h.err.String())
	}
}

func TestSafetyCLIUsage(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"trust"}, {"trust", "frob"}, {"deny"}, {"approve", "a", "b"}, {"trust", "add", "#x", "--case", "nope"}, {"trust", "add", "#x", "--for", "soon"}} {
		if code := h.run("", args...); code != 2 {
			t.Fatalf("%v: code %d", args, code)
		}
	}
	if h.connected {
		t.Fatal("usage errors reached Slack")
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"12h": 12 * time.Hour, "30d": 30 * 24 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := parseDuration(in); err != nil || got != want {
			t.Fatalf("%s: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"0d", "-1h", "d", "forever"} {
		if _, err := parseDuration(bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

func TestLoadDotEnvRefusesSafetySettings(t *testing.T) {
	for _, key := range []string{safety.IdentityEnv, safety.SafetyEnv} {
		env := fakeEnv{}
		path := writeDotEnv(t, key+"=soft\n")
		err := loadDotEnv(path, env.lookup, env.setenv)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s in .env: got %v", key, err)
		}
		if _, set := env[key]; set {
			t.Fatalf("%s was set from .env", key)
		}
	}
}
