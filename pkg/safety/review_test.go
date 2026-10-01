package safety

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBlockWithoutMembersFallsBackToConversation(t *testing.T) {
	w := openTest(t, Strict)
	out := block(t, w, Destination{Kind: DestDM, ConversationID: "D7", Name: "@dana"})
	if !out.Quarantined || len(out.Keys) != 1 || out.Keys[0] != Conversation("D7", "@dana") {
		t.Fatalf("DM with no members: %+v", out)
	}
	out = block(t, w, Destination{Kind: DestGroupDM, ConversationID: "G7", Name: "#mpdm-x"})
	if !out.Quarantined || out.Keys[0].ID != "G7" {
		t.Fatalf("group DM with no members: %+v", out)
	}
	if _, err := w.Quarantine.RecordBlock(Block{Destination: Destination{Kind: DestPerson}}); err == nil {
		t.Fatal("a person with no ID was accepted")
	}
	if _, err := w.Quarantine.RecordBlock(Block{Destination: Destination{Kind: DestChannel}}); err == nil {
		t.Fatal("a channel with no ID was accepted")
	}
}

func TestBlockNeverQuarantinesTheAccount(t *testing.T) {
	w := openTest(t, Strict)
	g := Destination{Kind: DestGroupDM, ConversationID: "G1", Members: []Key{Person(testOrg.UserID, "@me"), Person("U2", "@b")}}
	out := block(t, w, g)
	if len(out.Keys) != 1 || out.Keys[0].ID != "U2" {
		t.Fatalf("keys %v", out.Keys)
	}
	if _, q := w.Quarantine.State().IsQuarantined(testOrg.UserID); q {
		t.Fatal("the account's own user quarantined")
	}
	// A DM whose only member is the account is the self-DM.
	w2 := openTest(t, Strict)
	out = block(t, w2, dm("D0", testOrg.UserID, "@me"))
	if out.Quarantined || out.Warned || out.Strike != 1 {
		t.Fatalf("self DM: %+v", out)
	}
	if st := w2.Quarantine.State(); len(st.People)+len(st.Conversations) != 0 {
		t.Fatal("self DM quarantined")
	}
}

func TestJournalSameSizeSameMtimeEditRebuilds(t *testing.T) {
	w := openTest(t, Soft)
	os.WriteFile(qpath(w), []byte(blockLine(1, "CA")+"\n"), 0o600)
	w.Quarantine.State()
	fi, _ := os.Stat(qpath(w))
	f, _ := os.OpenFile(qpath(w), os.O_WRONLY, 0o600)
	f.WriteAt([]byte(blockLine(1, "CB")+"\n"), 0)
	f.Close()
	os.Chtimes(qpath(w), fi.ModTime(), fi.ModTime()) // mtime unchanged
	st := w.Quarantine.State()
	if _, ok := st.IsQuarantined("CB"); !ok {
		t.Fatal("same-size, same-mtime edit not read")
	}
	if _, ok := st.IsQuarantined("CA"); ok {
		t.Fatal("edited-away state survived")
	}
}

func TestJournalRefusesSymlink(t *testing.T) {
	w := openTest(t, Strict)
	target := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	os.WriteFile(target, []byte(blockLine(1, "C1")+"\n"), 0o600)
	if err := os.Symlink(target, qpath(w)); err != nil {
		t.Skip("no symlinks here:", err)
	}
	st := w.Quarantine.State()
	if st.Err == nil || !st.LockEngaged() {
		t.Fatalf("symlink read through: %+v", st)
	}
	if _, err := w.Quarantine.RecordBlock(Block{Destination: channel("C2", "#b")}); err == nil {
		t.Fatal("wrote through a symlink")
	}
	raw, _ := os.ReadFile(target)
	if bytes.Count(raw, []byte("\n")) != 1 {
		t.Fatal("symlink target modified")
	}
}

func TestOpenSweepsStaleRewrites(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, rewritePrefix+"123")
	fresh := filepath.Join(dir, rewritePrefix+"456")
	os.WriteFile(stale, []byte("held text"), 0o600)
	os.WriteFile(fresh, []byte("in progress"), 0o600)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(stale, old, old)
	if _, err := OpenDir(dir, testOrg, Strict); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale rewrite file kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("a rewrite in progress was swept")
	}
}

func TestCreateExpiresOldRequests(t *testing.T) {
	w := openTest(t, Strict)
	t0 := time.Now()
	w.Pending.Create(externalReq("expired held text"), t0)
	later := t0.Add(PendingTTL + time.Minute)
	if _, _, err := w.Pending.Create(externalReq("new"), later); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(w.Dir, PendingFile))
	if bytes.Contains(raw, []byte("expired held text")) {
		t.Fatal("Create left expired case 1 text on disk")
	}
	if !bytes.Contains(raw, []byte(`"text":"new"`)) {
		t.Fatal("new request missing")
	}
}

func TestTrustAddUsesStorePosture(t *testing.T) {
	dir := t.TempDir()
	soft, _ := OpenDir(dir, testOrg, Soft)
	if err := soft.Trust.Add(TrustAdd{Key: Conversation("C1", ""), Cases: []Case{CaseExternal}, Source: SourceElicitation, PendingID: "pabc"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, TrustFile))
	if !bytes.Contains(raw, []byte(`"posture":"soft"`)) {
		t.Fatalf("posture not the store's: %s", raw)
	}
	strict, _ := OpenDir(dir, testOrg, Strict)
	if err := strict.Trust.Add(TrustAdd{Key: Conversation("C2", ""), Cases: []Case{CaseExternal}, Source: SourceElicitation}); !errors.Is(err, ErrStrictElicitationTrust) {
		t.Fatalf("strict store accepted elicitation trust: %v", err)
	}
	if strict.Trust.State().InEffect(Conversation("C1", ""), time.Now()) {
		t.Fatal("strict reports an elicitation entry in effect")
	}
	if !soft.Trust.State().InEffect(Conversation("C1", ""), time.Now()) {
		t.Fatal("soft does not report its entry in effect")
	}
}

func TestTrustRemoveSurfacesUnreadable(t *testing.T) {
	w := openTest(t, Strict)
	os.Mkdir(filepath.Join(w.Dir, TrustFile), 0o700)
	if _, err := w.Trust.Remove(Conversation("C1", ""), time.Now()); err == nil {
		t.Fatal("remove on an unreadable file reported nothing")
	}
}

func TestBindingByPersonSurvivesDMOpen(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	person := Destination{Kind: DestPerson, Name: "@dana", Members: []Key{Person("U1", "@dana")}}
	r, _, _ := w.Pending.Create(Request{Cases: []Case{CaseExternal}, Tool: "say", Destination: person, ContentHash: "h"}, now)
	if _, err := w.Approve(r.ID, now); err != nil {
		t.Fatal(err)
	}
	// The DM now exists; the next call reaches it by conversation ID.
	opened := dm("D1", "U1", "@dana")
	if _, ok, err := w.Pending.ConsumeApproved(BindingFor(opened, "h", []Case{CaseExternal}), now); !ok || err != nil {
		t.Fatalf("approval lost when the DM opened: %v", err)
	}
}

// TestGateOrderConsumeApprovedBeforeCreate pins the documented order: the
// gate asks ConsumeApproved first and calls Create only when nothing is
// approved. Create alone does not see an approval.
func TestGateOrderConsumeApprovedBeforeCreate(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	req := externalReq("hi")
	r, _, _ := w.Pending.Create(req, now)
	w.Approve(r.ID, now)
	b := BindingFor(req.Destination, req.ContentHash, req.Cases)

	gate := func() (passed bool, id string) {
		if _, ok, err := w.Pending.ConsumeApproved(b, now); err != nil {
			t.Fatal(err)
		} else if ok {
			return true, ""
		}
		nr, _, err := w.Pending.Create(req, now)
		if err != nil {
			t.Fatal(err)
		}
		return false, nr.ID
	}
	if passed, _ := gate(); !passed {
		t.Fatal("approved call gated")
	}
	if passed, id := gate(); passed || id == r.ID {
		t.Fatal("second call passed on a used approval")
	}

	// The wrong order: Create first issues a new request beside the approval.
	r2, _, _ := w.Pending.Create(externalReq("other"), now)
	w.Approve(r2.ID, now)
	nr, created, _ := w.Pending.Create(externalReq("other"), now)
	if !created || nr.ID == r2.ID {
		t.Fatal("Create reused an approved request")
	}
}

func TestApproveRefusesResolvedLiftWithoutClearing(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	block(t, w, channel("C1", "#a"))
	lift, _, _ := w.Pending.Create(Request{Cases: []Case{CaseLift}, Tool: "say", Destination: channel("C1", "#a"), Lift: []Key{Conversation("C1", "#a")}}, now)
	w.Pending.Deny(lift.ID, AnswerCLI, now)
	if _, err := w.Approve(lift.ID, now); !errors.Is(err, ErrNotPending) {
		t.Fatalf("approved a denied lift: %v", err)
	}
	if _, q := w.Quarantine.State().IsQuarantined("C1"); !q {
		t.Fatal("a refused approval cleared the quarantine")
	}
}
