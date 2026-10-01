package features_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/safety"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// Boot writes the account's identity while handlers read it; under -race
// this overlaps them.
func TestSafetyIdentityReadsRaceBoot(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = ap.Provide()
	}()
	for i := 0; i < 200; i++ {
		ap.Organization()
		ap.ProvideIdentity()
		features.SafetyBanner(ap)
	}
	wg.Wait()
	srv.Quiesce(t)
	if team, _, _ := ap.Organization(); team != "T1" {
		t.Fatalf("team after boot = %q", team)
	}
}

// The strike lock is local: on a provider that has not booted, ADR-012's
// file checks still refuse with zero Slack calls.
func TestSafetyLocalChecksBeforeAnyBoot(t *testing.T) {
	srv := slacktest.New(t)
	ap := srv.Provider(t)
	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"../etc/passwd"}})
	wantIn(t, out, "is not a bare file name")
	if n := srv.TotalCalls(); n != 0 {
		t.Fatalf("a refused local check made %d Slack calls", n)
	}
}

// download fetches through a stand-in for files.slack.com; it returns
// what the download wrote and the call's output.
func download(t *testing.T, f *safetyFake, data []byte) string {
	t.Helper()
	return downloadThen(t, f, data, nil)
}

// downloadThen runs after once the bytes are written.
func downloadThen(t *testing.T, f *safetyFake, data []byte, after func()) string {
	t.Helper()
	t.Setenv("SLACK_MCP_EXCHANGE_DIR", "")
	f.srv.Handle("files.info", func(*http.Request) any {
		return map[string]any{"ok": true, "file": map[string]any{
			"id": "F7", "name": "q3.txt", "size": len(data),
			"url_private_download": "https://files.slack.com/files-pri/T1-F7/q3.txt",
			"channels":             []string{"C2"},
		}}
	})
	t.Cleanup(features.SetFetchFileForTest(func(_ string, w io.Writer) (int64, error) {
		n, err := w.Write(data)
		if after != nil {
			after()
		}
		return int64(n), err
	}))
	return runTool(t, features.Download, f.ap, map[string]any{"fileId": "F7"})
}

// End to end through download: the provenance hash is of the bytes
// written, so attaching that file elsewhere is a move from #platform.
func TestSafetyDownloadRecordsTheBytesWritten(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	uploads := attachUploads(t, f)
	data := []byte("quarterly numbers, as downloaded")
	wantIn(t, download(t, f, data), "Downloaded q3.txt")

	onDisk, err := os.ReadFile(filepath.Join(exchangeDir(), "q3.txt"))
	if err != nil || string(onDisk) != string(data) {
		t.Fatalf("exchange file %q, %v", onDisk, err)
	}
	sum := sha256.Sum256(onDisk)
	convs, found, err := f.workspace(t).Provenance.Lookup(hex.EncodeToString(sum[:]))
	if err != nil || !found || len(convs) != 1 || convs[0] != "C2" {
		t.Fatalf("provenance for the bytes written: %v %v %v", convs, found, err)
	}

	wantIn(t, f.say(t, context.Background(), map[string]any{"to": "#eng", "files": []any{"q3.txt"}}).Message,
		"Needs operator approval", "upload q3.txt from #platform to #eng")
	if uploads() != 0 {
		t.Fatalf("a gated move reached Slack")
	}
}

// breakProvenance makes the provenance file unreadable and unwritable: a
// directory where the file belongs.
func breakProvenance(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(safety.Dir("T1"), safety.ProvenanceFile), 0o700); err != nil {
		t.Fatal(err)
	}
}

// A download whose provenance cannot be recorded is deleted and fails, so
// no unrecorded file waits in the exchange directory to skip the move check.
func TestSafetyProvenanceRecordFailureFailsTheDownload(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	breakProvenance(t)
	wantIn(t, download(t, f, []byte("numbers")), "could not be recorded, so it was deleted")
	if _, err := os.Stat(filepath.Join(exchangeDir(), "q3.txt")); !os.IsNotExist(err) {
		t.Fatalf("the unrecorded download was kept: %v", err)
	}
}

// Provenance that cannot be read counts every attachment as moved.
func TestSafetyProvenanceUnreadableGatesAttachments(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	attachUploads(t, f)
	putExchange(t, "notes.txt", []byte("operator's own notes"))
	breakProvenance(t)
	wantIn(t, f.say(t, context.Background(), map[string]any{"to": "#eng", "files": []any{"notes.txt"}}).Message,
		"Needs operator approval", "from another conversation")
}

// Inside Enterprise Grid a sibling workspace's user is internal; another
// team outside the organization is external.
func TestSafetyGridSiblingIsInternal(t *testing.T) {
	f := newSafetyFakeWith(t, strictHuman(), func(srv *slacktest.Server) {
		srv.Handle("auth.test", func(*http.Request) any {
			return map[string]any{"ok": true, "url": srv.URL + "/", "team": "Praecipio", "user": "bockeliea",
				"team_id": "T1", "user_id": "U1", "enterprise_id": "E1"}
		})
	})
	if res := f.say(t, context.Background(), map[string]any{"to": "@grid", "text": "hi"}); !res.Success {
		t.Fatalf("a Grid sibling was gated: %+v", res)
	}
	wantIn(t, f.say(t, context.Background(), map[string]any{"to": "@outsider", "text": "hi"}).Message, "Needs operator approval")
}

// A group DM with one external member is external.
func TestSafetyGroupDMWithExternalMember(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	res := f.say(t, context.Background(), map[string]any{"to": "#mpdm-partner--bockeliea-1", "text": "hi"})
	wantIn(t, res.Message, "Needs operator approval")
	wantIn(t, res.Guidance, "outside your organization")
}

// Bulk mark-read skips a quarantined DM, names it, and marks the rest.
func TestSafetyBulkMarkReadSkipsQuarantined(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	f.say(t, context.Background(), map[string]any{"to": "@schen", "text": fakeToken()})

	f.srv.Handle("client.counts", func(*http.Request) any {
		return slacktest.Counts(nil, []any{
			slacktest.Conversation("D2", "1786752000.000000", "1786752114.508819", true, 0),
			slacktest.Conversation("D3", "1786752000.000000", "1786752114.508819", true, 0),
		})
	})
	var mu sync.Mutex
	var marked []string
	f.srv.Handle("conversations.mark", func(r *http.Request) any {
		_ = r.ParseForm()
		mu.Lock()
		marked = append(marked, r.Form.Get("channel"))
		mu.Unlock()
		return map[string]any{"ok": true}
	})
	out := runTool(t, features.MarkAsRead, f.ap, map[string]any{"target": "all-dms"})
	wantIn(t, out, "Marked 1 DMs as read", "Skipped, not marked: @schen (quarantined)")
	if len(marked) != 1 || marked[0] != "D3" {
		t.Fatalf("marked %v, want [D3]", marked)
	}
}

// An approval binds broadcast: the same reply sent to the channel too is
// different content.
func TestSafetyApprovalBindsBroadcast(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	reply := func(broadcast bool) *features.FeatureResult {
		return f.say(t, context.Background(), map[string]any{"to": "#partner", "text": "ok", "thread": "1786752000.000100", "broadcast": broadcast})
	}
	reply(false)
	ws := f.workspace(t)
	if _, err := ws.Approve(ws.Pending.List(time.Now())[0], time.Now()); err != nil {
		t.Fatalf("approve: %v", err)
	}
	wantIn(t, reply(true).Message, "Needs operator approval")
	if !reply(false).Success {
		t.Fatalf("the approved reply was refused")
	}
}

// A gated reaction's request shows the operator what is approved: the
// emoji, add or remove, and the message.
func TestSafetyGatedReactionShowsTheOperatorWhat(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	f.say(t, context.Background(), map[string]any{"to": "#partner", "emoji": "tada", "messageTs": "1786752114.508819", "remove": true})
	reqs := f.workspace(t).Pending.List(time.Now())
	if len(reqs) != 1 || reqs[0].Text != "reaction :tada: (remove) on the message at ts 1786752114.508819" {
		t.Fatalf("request: %+v", reqs)
	}
}

// Trust on a conversation applies only to the parties recorded with it, so
// one whose parties cannot be listed is never offered approve and trust.
func TestSafetyApproveAndTrustNeedsParties(t *testing.T) {
	f := newSafetyFake(t, safety.Settings{Identity: safety.Human, Posture: safety.Soft})
	offer := features.WithElicitation(context.Background(), features.Elicitation{Offer: true})
	choices := func(to string) string {
		return strings.Join(f.say(t, offer, map[string]any{"to": to, "text": "x"}).InputRequest.Choices, ",")
	}
	if got := choices("#partner"); got != "approve-once,approve-and-trust,deny" {
		t.Fatalf("#partner offers %q", got)
	}
	if got := choices("#lonely"); got != "approve-once,deny" {
		t.Fatalf("#lonely offers %q", got)
	}
}

// A block that cannot be recorded holds every write in this process until
// the operator approves the lift request issued with it.
func TestSafetyUnrecordedBlockHoldsWrites(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	lock := filepath.Join(safety.Dir("T1"), safety.QuarantineFile+".lock")
	if err := os.MkdirAll(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	res := f.say(t, context.Background(), map[string]any{"to": "#eng", "text": fakeToken()})
	wantIn(t, res.Message, "BLOCKED", "could not be recorded", "until the operator approves pending p", "or clears the strikes, or the server restarts")

	wantIn(t, f.say(t, context.Background(), map[string]any{"to": "#platform", "text": "clean"}).Message, "an earlier block could not be recorded")
	wantIn(t, runTool(t, features.MarkAsRead, f.ap, map[string]any{"channel": "#platform"}), "an earlier block could not be recorded")
	wantIn(t, features.SafetyBanner(f.ap), "An earlier block could not be recorded")
	if len(f.posted()) != 0 {
		t.Fatalf("a held write posted")
	}

	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	ws := f.workspace(t)
	reqs := ws.Pending.List(time.Now())
	if len(reqs) != 1 || !reqs[0].IsLift() {
		t.Fatalf("want one lift request, got %+v", reqs)
	}
	if _, err := ws.Approve(reqs[0], time.Now()); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !f.say(t, context.Background(), map[string]any{"to": "#platform", "text": "clean"}).Success {
		t.Fatalf("the hold outlived the approved lift")
	}
}

// When the record fails and so does removing the file, the download says
// the file is still there without a record.
func TestSafetyProvenanceFailureReportsAKeptFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSafetyFake(t, strictHuman())
	breakProvenance(t)
	dir := exchangeDir()
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	out := downloadThen(t, f, []byte("numbers"), func() {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Errorf("chmod: %v", err)
		}
	})
	_ = os.Chmod(dir, 0o700)
	wantIn(t, out, "removing it failed too", "still in the exchange directory without a record")
}

// A malformed provenance line may be any file's record, so an attachment
// with no record counts as moved while one is skipped.
func TestSafetyMalformedProvenanceGatesUnrecorded(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	attachUploads(t, f)
	putExchange(t, "notes.txt", []byte("operator's own notes"))
	if err := os.MkdirAll(safety.Dir("T1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(safety.Dir("T1"), safety.ProvenanceFile), []byte("{\"sha256\": torn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := f.say(t, context.Background(), map[string]any{"to": "#eng", "files": []any{"notes.txt"}})
	wantIn(t, res.Message, "Needs operator approval", "from another conversation")
	if strings.Contains(res.Message+res.Guidance, "provenance") {
		t.Fatalf("the agent was told about the provenance file:\n%s", res.Message)
	}
	// The operator's listing says why: the file needs repair.
	reqs := f.workspace(t).Pending.List(time.Now())
	if len(reqs) != 1 || len(reqs[0].From) != 1 || !strings.Contains(reqs[0].From[0], "provenance.jsonl has a malformed line") {
		t.Fatalf("request: %+v", reqs)
	}
}

// slack-mcp quarantine clear strikes, from another process, releases the
// hold even though the unrecorded block left no strike in the file.
func TestSafetyStrikesClearReleasesTheHold(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	lock := filepath.Join(safety.Dir("T1"), safety.QuarantineFile+".lock")
	if err := os.MkdirAll(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	f.say(t, context.Background(), map[string]any{"to": "#eng", "text": fakeToken()})
	wantIn(t, f.say(t, context.Background(), map[string]any{"to": "#platform", "text": "clean"}).Message, "an earlier block could not be recorded")

	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if _, err := f.workspace(t).Quarantine.Clear(safety.StrikesKey, safety.ByCLI, "", time.Now()); err != nil {
		t.Fatalf("clear strikes: %v", err)
	}
	if !f.say(t, context.Background(), map[string]any{"to": "#platform", "text": "clean"}).Success {
		t.Fatalf("clear strikes did not release the hold")
	}
}
