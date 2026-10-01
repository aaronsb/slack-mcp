package safety

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"
)

func TestPostureTables(t *testing.T) {
	type step struct {
		quarantined, warned, lock bool
	}
	cases := []struct {
		posture Posture
		steps   []step
	}{
		{Strict, []step{
			{quarantined: true},
			{quarantined: true, lock: true},
			{quarantined: true, lock: true},
		}},
		{Soft, []step{
			{warned: true},
			{quarantined: true},
			{quarantined: true, lock: true},
		}},
	}
	for _, tc := range cases {
		t.Run(string(tc.posture), func(t *testing.T) {
			w := openTest(t, tc.posture)
			dests := []Destination{channel("C1", "#a"), dm("D1", "U1", "@dana"), channel("C3", "#c")}
			for i, want := range tc.steps {
				out := block(t, w, dests[i])
				if out.Strike != i+1 || out.Quarantined != want.quarantined || out.Warned != want.warned || out.LockEngaged != want.lock {
					t.Fatalf("block %d: %+v, want %+v", i+1, out, want)
				}
				st := w.Quarantine.State()
				if st.Strikes != i+1 || st.LockEngaged() != want.lock {
					t.Fatalf("block %d state: strikes=%d locked=%v", i+1, st.Strikes, st.LockEngaged())
				}
				if got := len(st.Check(dests[i])) > 0; got != want.quarantined {
					t.Fatalf("block %d: Check quarantined=%v", i+1, got)
				}
			}
		})
	}
}

func TestSoftFirstBlockRecordedAsUnquarantinedBlock(t *testing.T) {
	w := openTest(t, Soft)
	block(t, w, channel("C1", "#a"))
	raw, _ := os.ReadFile(qpath(w))
	if !bytes.Contains(raw, []byte(`"posture":"soft","quarantined":false`)) {
		t.Fatalf("entry: %s", raw)
	}
	// The second block, on the same destination, quarantines it.
	if out := block(t, w, channel("C1", "#a")); !out.Quarantined {
		t.Fatal("soft second block on the first destination must quarantine it")
	}
}

func TestSoftSecondBlockLeavesFirstDestinationOpen(t *testing.T) {
	w := openTest(t, Soft)
	block(t, w, channel("C1", "#a"))
	block(t, w, channel("C2", "#b"))
	st := w.Quarantine.State()
	if _, ok := st.IsQuarantined("C1"); ok {
		t.Fatal("first destination closed")
	}
	if _, ok := st.IsQuarantined("C2"); !ok {
		t.Fatal("second destination open")
	}
}

func TestSelfDMNeverQuarantinedButCountsStrike(t *testing.T) {
	for _, p := range []Posture{Strict, Soft} {
		w := openTest(t, p)
		self := Destination{Kind: DestSelf, ConversationID: "DSELF", Name: "@me"}
		out := block(t, w, self)
		if out.Quarantined || len(out.Keys) != 0 || out.Strike != 1 {
			t.Fatalf("%s: %+v", p, out)
		}
		out = block(t, w, self)
		if out.Quarantined || out.Strike != 2 {
			t.Fatalf("%s second: %+v", p, out)
		}
		st := w.Quarantine.State()
		if len(st.Check(self)) != 0 || len(st.People)+len(st.Conversations) != 0 {
			t.Fatalf("%s: self quarantined", p)
		}
		if p == Strict && !st.LockEngaged() {
			t.Fatal("self-DM blocks still count toward the lock")
		}
	}
}

func TestGroupDMQuarantinesEachMemberAndPersonCoversDMs(t *testing.T) {
	w := openTest(t, Strict)
	g := Destination{Kind: DestGroupDM, ConversationID: "G1", Name: "#mpdm-a--b", Members: []Key{Person("U1", "@a"), Person("U2", "@b")}}
	block(t, w, g)
	st := w.Quarantine.State()
	if len(st.People) != 2 || len(st.Conversations) != 0 {
		t.Fatalf("people=%v convs=%v", st.People, st.Conversations)
	}
	// Any DM with @a is closed, however reached.
	if len(st.Check(dm("D9", "U1", "@a"))) == 0 {
		t.Fatal("DM with a quarantined person open")
	}
	if len(st.Check(Destination{Kind: DestPerson, Members: []Key{Person("U2", "@b")}})) == 0 {
		t.Fatal("person with no DM open")
	}
	// Their public channels stay writable.
	if len(st.Check(channel("C1", "#general"))) != 0 {
		t.Fatal("channel closed")
	}
}

func TestClearQuarantineAndStrikes(t *testing.T) {
	w := openTest(t, Strict)
	block(t, w, channel("C1", "#a"))
	block(t, w, dm("D1", "U1", "@dana"))
	now := time.Now()
	if ok, err := w.Quarantine.Clear(Conversation("C1", "#a"), ByCLI, "", now); err != nil || !ok {
		t.Fatalf("clear C1: %v %v", ok, err)
	}
	if ok, _ := w.Quarantine.Clear(Conversation("C1", "#a"), ByCLI, "", now); ok {
		t.Fatal("second clear reported clearing")
	}
	st := w.Quarantine.State()
	if _, q := st.IsQuarantined("C1"); q {
		t.Fatal("C1 still quarantined")
	}
	if !st.LockEngaged() || st.Strikes != 2 {
		t.Fatal("clearing a quarantine must not touch strikes")
	}
	w.Quarantine.Clear(StrikesKey, ByCLI, "", now)
	st = w.Quarantine.State()
	if st.LockEngaged() || st.Strikes != 0 {
		t.Fatalf("after clear strikes: %+v", st)
	}
	if _, q := st.IsQuarantined("U1"); !q {
		t.Fatal("clear strikes lifted a quarantine")
	}
	// The file is a history: blocks remain.
	raw, _ := os.ReadFile(qpath(w))
	if bytes.Count(raw, []byte(`"kind":"block"`)) != 2 {
		t.Fatal("clear deleted a block")
	}
}

func TestLockRecordedSurvivesPostureRelaxation(t *testing.T) {
	dir := t.TempDir()
	strict, _ := OpenDir(dir, testOrg, Strict)
	block(t, strict, channel("C1", "#a"))
	block(t, strict, channel("C2", "#b"))
	soft, _ := OpenDir(dir, testOrg, Soft)
	if !soft.Quarantine.State().LockEngaged() {
		t.Fatal("switching to soft lifted a lock strict engaged")
	}
}

func TestRecordsHoldNoMatchedValue(t *testing.T) {
	w := openTest(t, Strict)
	secret := "xox" + "b-1234567890-" + strings.Repeat("q", 20) // built at run time: a literal trips push protection
	w.Quarantine.RecordBlock(Block{
		Destination: channel("C1", "#a"),
		Call:        CallRecord{Tool: "say", TextSHA256: HashText("token " + secret), Files: []FileRecord{{NameSHA256: HashText("creds.txt"), Size: 10}}},
		Match:       MatchRecord{Class: "slack-token", Location: "text", Offset: 6, Chain: []string{"base64"}},
	})
	raw, _ := os.ReadFile(qpath(w))
	if bytes.Contains(raw, []byte("xoxb")) || bytes.Contains(raw, []byte("creds.txt")) {
		t.Fatalf("value leaked: %s", raw)
	}
}

// --- trust ---

func addTrust(t *testing.T, w *Workspace, a TrustAdd) {
	t.Helper()
	if a.Source == "" {
		a.Source = SourceCLI
	}
	if err := w.Trust.Add(a); err != nil {
		t.Fatal(err)
	}
}

func TestTrustCaseBinding(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	addTrust(t, w, TrustAdd{Key: Conversation("C1", "#partner"), Cases: []Case{CaseExternal}, Parties: []string{"TACME"}})
	ts := w.Trust.State()
	d := channel("C1", "#partner")
	if _, ok := ts.Trusted(TrustQuery{Destination: d, Case: CaseExternal, Parties: []string{"TACME"}, PartiesKnown: true}, now); !ok {
		t.Fatal("case 1 not trusted")
	}
	if _, ok := ts.Trusted(TrustQuery{Destination: d, Case: CaseCrossConversation}, now); ok {
		t.Fatal("case 1 entry trusted case 2")
	}
	if _, ok := ts.Trusted(TrustQuery{Destination: d, Case: CaseLift}, now); ok {
		t.Fatal("case 3 trusted")
	}
	// A later add replaces the cases: narrowing works.
	addTrust(t, w, TrustAdd{Key: Conversation("C1", "#partner"), Cases: []Case{CaseCrossConversation}})
	ts = w.Trust.State()
	if _, ok := ts.Trusted(TrustQuery{Destination: d, Case: CaseExternal, Parties: []string{"TACME"}, PartiesKnown: true}, now); ok {
		t.Fatal("replaced entry still trusts case 1")
	}
	if _, ok := ts.Trusted(TrustQuery{Destination: d, Case: CaseCrossConversation}, now); !ok {
		t.Fatal("replacement not in effect")
	}
}

func TestTrustPartySubsetRule(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	addTrust(t, w, TrustAdd{Key: Conversation("C1", "#partner"), Cases: []Case{CaseExternal}, Parties: []string{"TACME", "TBETA"}})
	ts := w.Trust.State()
	d := channel("C1", "#partner")
	q := func(parties []string, known bool) bool {
		_, ok := ts.Trusted(TrustQuery{Destination: d, Case: CaseExternal, Parties: parties, PartiesKnown: known}, now)
		return ok
	}
	if !q([]string{"TACME"}, true) || !q([]string{"TBETA", "TACME"}, true) {
		t.Fatal("subset not trusted")
	}
	if q([]string{"TACME", "TNEW"}, true) {
		t.Fatal("new organization passed")
	}
	if q(nil, true) {
		t.Fatal("external flag with no party passed")
	}
	if q([]string{"TACME"}, false) {
		t.Fatal("failed enumeration passed")
	}
}

func TestTrustPersonAndGroupDM(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	addTrust(t, w, TrustAdd{Key: Person("UEXT1", "@ext1"), Cases: []Case{CaseExternal, CaseCrossConversation}})
	ts := w.Trust.State()
	if _, ok := ts.Trusted(TrustQuery{Destination: dm("D1", "UEXT1", "@ext1"), Case: CaseExternal}, now); !ok {
		t.Fatal("person entry does not cover their DM")
	}
	if _, ok := ts.Trusted(TrustQuery{Destination: Destination{Kind: DestPerson, Members: []Key{Person("UEXT1", "")}}, Case: CaseExternal}, now); !ok {
		t.Fatal("person with no DM not covered")
	}
	g := Destination{Kind: DestGroupDM, ConversationID: "G1", Members: []Key{Person("UEXT1", ""), Person("UINT", "")}}
	// Case 1: the other member is internal, which stands in for trust.
	if _, ok := ts.Trusted(TrustQuery{Destination: g, Case: CaseExternal, Internal: map[string]bool{"UINT": true}}, now); !ok {
		t.Fatal("group DM of trusted + internal not covered for case 1")
	}
	// Case 2: internal does not stand in.
	if _, ok := ts.Trusted(TrustQuery{Destination: g, Case: CaseCrossConversation, Internal: map[string]bool{"UINT": true}}, now); ok {
		t.Fatal("internal stood in for case 2")
	}
	// An untrusted external member gates it.
	g.Members = append(g.Members, Person("UEXT2", ""))
	if _, ok := ts.Trusted(TrustQuery{Destination: g, Case: CaseExternal, Internal: map[string]bool{"UINT": true}}, now); ok {
		t.Fatal("untrusted external member passed")
	}
}

func TestTrustStrictIgnoresElicitationEntries(t *testing.T) {
	dir := t.TempDir()
	soft, _ := OpenDir(dir, testOrg, Soft)
	strict, _ := OpenDir(dir, testOrg, Strict)
	now := time.Now()
	d := channel("C1", "#team")

	// A CLI entry for case 2, then an elicitation entry for both cases.
	addTrust(t, soft, TrustAdd{Key: Conversation("C1", "#team"), Cases: []Case{CaseCrossConversation}, Source: SourceCLI})
	addTrust(t, soft, TrustAdd{Key: Conversation("C1", "#team"), Cases: []Case{CaseExternal, CaseCrossConversation}, Source: SourceElicitation, PendingID: "p7k2", Parties: []string{"TX"}})

	ext := TrustQuery{Destination: d, Case: CaseExternal, Parties: []string{"TX"}, PartiesKnown: true}
	if _, ok := soft.Trust.State().Trusted(ext, now); !ok {
		t.Fatal("soft ignores its own elicitation entry")
	}
	sts := strict.Trust.State()
	if _, ok := sts.Trusted(ext, now); ok {
		t.Fatal("strict honored an elicitation entry")
	}
	// The ignored entry replaces nothing: the CLI entry stays in effect.
	e, ok := sts.Trusted(TrustQuery{Destination: d, Case: CaseCrossConversation}, now)
	if !ok || e.Source != SourceCLI {
		t.Fatalf("strict lost the CLI entry: %+v %v", e, ok)
	}
	list := sts.List(now, strict.Quarantine.State())
	states := map[TrustSource]string{}
	for _, l := range list {
		states[l.Entry.Source] = l.State
	}
	if states[SourceElicitation] != TrustIgnored || states[SourceCLI] != TrustInEffect {
		t.Fatalf("listing: %+v", states)
	}

	// Strict refuses to add by elicitation at all.
	err := strict.Trust.Add(TrustAdd{Key: Conversation("C2", ""), Cases: []Case{CaseExternal}, Source: SourceElicitation})
	if !errors.Is(err, ErrStrictElicitationTrust) {
		t.Fatalf("got %v", err)
	}
}

func TestTrustRemoveExpiryAndListStates(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	past := now.Add(-time.Hour)
	addTrust(t, w, TrustAdd{Key: Conversation("C1", "#a"), Cases: []Case{CaseCrossConversation}, Expires: &past})
	addTrust(t, w, TrustAdd{Key: Conversation("C2", "#b"), Cases: []Case{CaseCrossConversation}})
	addTrust(t, w, TrustAdd{Key: Conversation("C3", "#c"), Cases: []Case{CaseCrossConversation}})
	block(t, w, channel("C2", "#b"))

	ts := w.Trust.State()
	if _, ok := ts.Trusted(TrustQuery{Destination: channel("C1", "#a"), Case: CaseCrossConversation}, now); ok {
		t.Fatal("expired entry in effect")
	}
	got := map[string]string{}
	for _, l := range ts.List(now, w.Quarantine.State()) {
		got[l.Entry.Key.ID] = l.State
	}
	want := map[string]string{"C1": TrustExpired, "C2": TrustQuarantined, "C3": TrustInEffect}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %q, want %q (all %v)", k, got[k], v, got)
		}
	}

	ok, err := w.Trust.Remove(Conversation("C3", ""), now)
	if err != nil || !ok {
		t.Fatalf("remove: %v %v", ok, err)
	}
	if _, ok := w.Trust.State().Trusted(TrustQuery{Destination: channel("C3", "#c"), Case: CaseCrossConversation}, now); ok {
		t.Fatal("removed entry in effect")
	}
	if ok, _ := w.Trust.Remove(Conversation("C3", ""), now); ok {
		t.Fatal("removed twice")
	}
}

func TestTrustUseIsHistoryOnly(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	addTrust(t, w, TrustAdd{Key: Conversation("C1", "#a"), Cases: []Case{CaseCrossConversation}})
	ts := w.Trust.State()
	e, _ := ts.Trusted(TrustQuery{Destination: channel("C1", "#a"), Case: CaseCrossConversation}, now)
	if err := w.Trust.RecordUse(TrustUse{Entry: e, Destination: channel("C1", "#a"), Cases: []Case{CaseCrossConversation}}); err != nil {
		t.Fatal(err)
	}
	ts = w.Trust.State()
	if _, ok := ts.Trusted(TrustQuery{Destination: channel("C1", "#a"), Case: CaseCrossConversation}, now); !ok {
		t.Fatal("a use changed trust")
	}
	raw, _ := os.ReadFile(filepath.Join(w.Dir, TrustFile))
	if !bytes.Contains(raw, []byte(`"kind":"use"`)) {
		t.Fatal("no use entry")
	}
}

// --- pending ---

func externalReq(text string) Request {
	return Request{
		Cases: []Case{CaseExternal}, Tool: "say", Destination: channel("C1", "#partner"),
		ContentHash: HashContent([]byte(text)), Text: text, FileNames: []string{"a.txt"}, FileCount: 1,
	}
}

func TestPendingCreateRepeatAndDeny(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	r1, created, err := w.Pending.Create(externalReq("hi"), now)
	if err != nil || !created || !strings.HasPrefix(r1.ID, "p") || len(r1.ID) != 4 {
		t.Fatalf("create: %+v %v %v", r1, created, err)
	}
	if !r1.Expires.Equal(r1.Created.Add(PendingTTL)) {
		t.Fatal("expiry not 24h")
	}
	r2, created, _ := w.Pending.Create(externalReq("hi"), now)
	if created || r2.ID != r1.ID {
		t.Fatal("repeat named a new ID")
	}
	r3, created, _ := w.Pending.Create(externalReq("changed"), now)
	if !created || r3.ID == r1.ID {
		t.Fatal("changed content reused the ID")
	}
	if _, err := w.Pending.Deny(r1, AnswerCLI, now); err != nil {
		t.Fatal(err)
	}
	r4, created, _ := w.Pending.Create(externalReq("hi"), now)
	if !created || r4.ID == r1.ID {
		t.Fatal("repeat after denial did not issue a new request")
	}
}

func TestPendingContentHeldForCase1Only(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	r := externalReq("secret-ish text")
	r.Cases = []Case{CaseCrossConversation}
	got, _, _ := w.Pending.Create(r, now)
	if got.Text != "" || got.FileNames != nil {
		t.Fatal("case 2 request held content")
	}
	raw, _ := os.ReadFile(filepath.Join(w.Dir, PendingFile))
	if bytes.Contains(raw, []byte("secret-ish")) {
		t.Fatal("content written for case 2")
	}
}

func TestPendingCLIApprovalSingleUse(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	r, _, _ := w.Pending.Create(externalReq("hi"), now)
	if _, err := w.Approve(r, now); err != nil {
		t.Fatal(err)
	}
	b := Binding{DestinationID: "C1", ContentHash: HashContent([]byte("hi")), Cases: []Case{CaseExternal}}
	if _, ok, _ := w.Pending.ConsumeApproved(Binding{DestinationID: "C1", ContentHash: HashContent([]byte("other")), Cases: b.Cases}, now); ok {
		t.Fatal("approval used for different content")
	}
	if _, ok, err := w.Pending.ConsumeApproved(b, now); !ok || err != nil {
		t.Fatalf("approved call not let through: %v", err)
	}
	if _, ok, _ := w.Pending.ConsumeApproved(b, now); ok {
		t.Fatal("one approval let two sends through")
	}
	if _, err := w.Approve(r, now); !errors.Is(err, ErrNotPending) {
		t.Fatalf("re-approve: %v", err)
	}
}

func TestPendingConsumeRaceFirstWins(t *testing.T) {
	dir := t.TempDir()
	a, _ := OpenDir(dir, testOrg, Strict)
	b, _ := OpenDir(dir, testOrg, Strict)
	now := time.Now()
	r, _, _ := a.Pending.Create(externalReq("hi"), now)
	bind := Binding{DestinationID: "C1", ContentHash: r.ContentHash, Cases: r.Cases}
	if _, err := a.Pending.Consume(r.ID, bind, AnswerElicitation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Pending.Consume(r.ID, bind, AnswerElicitation, now); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second consumer: %v", err)
	}
	if _, err := a.Pending.Consume("pzzz", bind, AnswerElicitation, now); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestPendingConsumeChecksBinding(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	r, _, _ := w.Pending.Create(externalReq("hi"), now)
	bad := Binding{DestinationID: "C2", ContentHash: r.ContentHash, Cases: r.Cases}
	if _, err := w.Pending.Consume(r.ID, bad, AnswerElicitation, now); !errors.Is(err, ErrMismatch) {
		t.Fatalf("got %v", err)
	}
	bad = Binding{DestinationID: "C1", ContentHash: r.ContentHash, Cases: []Case{CaseExternal, CaseCrossConversation}}
	if _, err := w.Pending.Consume(r.ID, bad, AnswerElicitation, now); !errors.Is(err, ErrMismatch) {
		t.Fatalf("cases: %v", err)
	}
}

func TestPendingExpiry(t *testing.T) {
	w := openTest(t, Strict)
	t0 := time.Now()
	r, _, _ := w.Pending.Create(externalReq("held text"), t0)
	keep, _, _ := w.Pending.Create(externalReq("fresh"), t0.Add(23*time.Hour))
	later := t0.Add(PendingTTL)

	got, ok := w.Pending.Lookup(r.ID, later)
	if !ok || got.Status != StatusExpired || got.Text != "" {
		t.Fatalf("expired lookup: %+v", got)
	}
	if _, err := w.Approve(r, later); !errors.Is(err, ErrNotPending) {
		t.Fatalf("approved an expired request: %v", err)
	}
	bind := Binding{DestinationID: "C1", ContentHash: r.ContentHash, Cases: r.Cases}
	if _, err := w.Pending.Consume(r.ID, bind, AnswerElicitation, later); !errors.Is(err, ErrNotPending) {
		t.Fatalf("consumed an expired request: %v", err)
	}
	if ids := w.Pending.List(later); len(ids) != 1 || ids[0].ID != keep.ID {
		t.Fatalf("list: %+v", ids)
	}

	n, err := w.Pending.Expire(later)
	if err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	raw, _ := os.ReadFile(filepath.Join(w.Dir, PendingFile))
	if bytes.Contains(raw, []byte("held text")) {
		t.Fatal("expired content still on disk")
	}
	if !bytes.Contains(raw, []byte("fresh")) {
		t.Fatal("live request dropped")
	}
	if _, ok := w.Pending.Lookup(keep.ID, later); !ok {
		t.Fatal("live request lost after rewrite")
	}
	// Another handle rebuilds from the replaced file.
	w2, _ := OpenDir(w.Dir, testOrg, Strict)
	if _, ok := w2.Pending.Lookup(r.ID, later); ok {
		t.Fatal("expired request survived")
	}
}

func TestPendingLiftApprovalClears(t *testing.T) {
	w := openTest(t, Strict)
	now := time.Now()
	block(t, w, channel("C1", "#a"))
	block(t, w, dm("D1", "U1", "@dana"))
	lift, _, err := w.IssueLift(Request{Cases: []Case{CaseLift}, Tool: "say", Destination: channel("C1", "#a"), Lift: []Key{Conversation("C1", "#a")}}, now)
	if err != nil {
		t.Fatal(err)
	}
	lock, _, _ := w.IssueLift(Request{Cases: []Case{CaseLift}, Tool: "say", Destination: channel("C5", "#e"), Lift: []Key{StrikesKey}}, now)
	if _, err := w.Approve(lift, now); err != nil {
		t.Fatal(err)
	}
	st := w.Quarantine.State()
	if _, q := st.IsQuarantined("C1"); q {
		t.Fatal("lift approval did not clear")
	}
	if !st.LockEngaged() {
		t.Fatal("lifting a quarantine lifted the lock")
	}
	got, _ := w.Pending.Lookup(lift.ID, now)
	if got.Status != StatusConsumed {
		t.Fatalf("lift status %s", got.Status)
	}
	// A lift lets no call through.
	if _, err := w.Pending.Consume(lift.ID, Binding{DestinationID: "C1"}, AnswerCLI, now); err == nil {
		t.Fatal("lift consumed as a send")
	}
	w.Approve(lock, now)
	if w.Quarantine.State().LockEngaged() {
		t.Fatal("strike lift did not clear the lock")
	}
	raw, _ := os.ReadFile(qpath(w))
	if !bytes.Contains(raw, []byte(`"by":"approval","pending_id":"`+lift.ID+`"`)) {
		t.Fatalf("clear does not name the approval: %s", raw)
	}
}

// --- request state ---

func TestRequestStateSignVerify(t *testing.T) {
	s, _ := NewSigner()
	now := time.Now()
	r := externalReq("hi")
	r.ID = "p7k2"
	tok, err := s.Sign(r, ChoicesFor(Strict), now)
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{DestinationID: "C1", ContentHash: r.ContentHash, Cases: r.Cases}

	// Tamper: flip a byte of the payload.
	bad := []byte(tok)
	bad[3] ^= 1
	if _, err := s.Verify(string(bad), "p7k2", b, ChoiceApproveOnce, now); !errors.Is(err, ErrStateTampered) && !errors.Is(err, ErrStateMalformed) {
		t.Fatalf("tampered: %v", err)
	}
	// Another process's key (a restart).
	other, _ := NewSigner()
	if _, err := other.Verify(tok, "p7k2", b, ChoiceApproveOnce, now); !errors.Is(err, ErrStateTampered) {
		t.Fatalf("restart: %v", err)
	}
	// Another call.
	if _, err := s.Verify(tok, "p7k2", Binding{DestinationID: "C2", ContentHash: r.ContentHash, Cases: r.Cases}, ChoiceApproveOnce, now); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := s.Verify(tok, "pxxx", b, ChoiceApproveOnce, now); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("pending id: %v", err)
	}
	// Expiry.
	if _, err := s.Verify(tok, "p7k2", b, ChoiceApproveOnce, now.Add(RequestStateTTL)); !errors.Is(err, ErrStateExpired) {
		t.Fatalf("expired: %v", err)
	}
	// A choice the strict form did not offer.
	if _, err := s.Verify(tok, "p7k2", b, ChoiceApproveTrust, now); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("unoffered choice: %v", err)
	}
	// Valid once, then a replay.
	st, err := s.Verify(tok, "p7k2", b, ChoiceApproveOnce, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if st.Offers(ChoiceApproveTrust) || !st.Offers(ChoiceApproveOnce) {
		t.Fatal("strict form offered trust")
	}
	if _, err := s.Verify(tok, "p7k2", b, ChoiceApproveOnce, now.Add(2*time.Minute)); !errors.Is(err, ErrStateReplayed) {
		t.Fatalf("replay: %v", err)
	}
	if !(len(ChoicesFor(Soft)) == 3) {
		t.Fatal("soft offers approve and trust")
	}
}

func TestRequestStateReplayFindsNothingToApprove(t *testing.T) {
	w := openTest(t, Soft)
	s, _ := NewSigner()
	now := time.Now()
	r, _, _ := w.Pending.Create(externalReq("hi"), now)
	tok, _ := s.Sign(r, ChoicesFor(Soft), now)
	b := Binding{DestinationID: "C1", ContentHash: r.ContentHash, Cases: r.Cases}
	if _, err := s.Verify(tok, r.ID, b, ChoiceApproveOnce, now); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Pending.Consume(r.ID, b, AnswerElicitation, now); err != nil {
		t.Fatal(err)
	}
	// Even a second signer state for the same request finds it consumed.
	tok2, _ := s.Sign(r, ChoicesFor(Soft), now)
	if _, err := s.Verify(tok2, r.ID, b, ChoiceApproveOnce, now); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Pending.Consume(r.ID, b, AnswerElicitation, now); !errors.Is(err, ErrNotPending) {
		t.Fatalf("replayed approval: %v", err)
	}
}

// --- settings ---

func TestSettingsFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	s, err := SettingsFromEnv(env(nil))
	if err != nil || s != DefaultSettings || s.Identity != Human || s.Posture != Strict {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	s, err = SettingsFromEnv(env(map[string]string{IdentityEnv: " Agent ", SafetyEnv: "SOFT"}))
	if err != nil || s.Identity != Agent || s.Posture != Soft {
		t.Fatalf("set: %+v %v", s, err)
	}
	for _, bad := range []map[string]string{
		{IdentityEnv: "bot"},
		{SafetyEnv: "lenient"},
		{SafetyEnv: "off"},
	} {
		if _, err := SettingsFromEnv(env(bad)); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	_, err = SettingsFromEnv(env(map[string]string{IdentityEnv: "robot"}))
	if err == nil || !strings.Contains(err.Error(), IdentityEnv) || !strings.Contains(err.Error(), "human or agent") {
		t.Fatalf("error does not name setting and values: %v", err)
	}
}

func TestCurrentSettings(t *testing.T) {
	if Current() != DefaultSettings && current.Load() == nil {
		t.Fatal("unset Current is not the default")
	}
	SetCurrent(Settings{Identity: Agent, Posture: Soft})
	defer current.Store(nil)
	if Current().Posture != Soft {
		t.Fatal("SetCurrent not visible")
	}
}

// --- org ---

func TestOrgClassification(t *testing.T) {
	o := Org{TeamID: "T1", EnterpriseID: "E1", UserID: "UME"}
	u := func(team, ent string, stranger bool) *slack.User {
		x := &slack.User{ID: "UX", TeamID: team, IsStranger: stranger}
		x.Enterprise.EnterpriseID = ent
		return x
	}
	if o.UserExternal(u("T1", "", false)) {
		t.Fatal("own team external")
	}
	if o.UserExternal(u("T2", "E1", false)) {
		t.Fatal("Grid sibling external")
	}
	if !o.UserExternal(u("T2", "E2", false)) || !o.UserExternal(u("T1", "", true)) || !o.UserExternal(u("", "", false)) {
		t.Fatal("external not detected")
	}
	if !(Org{TeamID: "T1"}).UserExternal(u("T2", "", false)) {
		t.Fatal("outside Grid any other team is external")
	}

	ch := &slack.Channel{}
	ch.IsShared, ch.IsOrgShared = true, true
	if ChannelExternal(ch) {
		t.Fatal("org-shared external")
	}
	ch.IsOrgShared = false
	if !ChannelExternal(ch) {
		t.Fatal("ambiguous shared not external")
	}
	ch.SharedTeamIDs = []string{"T1", "T2", "TACME"}
	ch.PendingShared = []string{"TNEW"}
	ch.InternalTeamIDs = []string{"T2"}
	if got := strings.Join(o.ChannelParties(ch), ","); got != "TACME,TNEW" {
		t.Fatalf("parties %s", got)
	}
	if got := strings.Join((Org{TeamID: "T1"}).ChannelParties(ch), ","); got != "T2,TACME,TNEW" {
		t.Fatalf("parties outside Grid %s", got)
	}
}
