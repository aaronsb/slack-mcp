package features_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/safety"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

// Scheduled send (ADR-016) end to end: say at=, messages scheduled=true,
// say cancel=, against a fake drafts API.

// scheduleNow is 2027-01-15 12:00 UTC (05:00 in Denver, MST).
var scheduleNow = time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)

type draftsFake struct {
	mu      sync.Mutex
	creates []url.Values
	deletes []url.Values
	list    []map[string]any
	hasMore bool
	// createErr and deleteErrs make the next calls fail with Slack errors.
	createErr  string
	deleteErrs []string
}

func (d *draftsFake) install(srv *slacktest.Server) {
	srv.Handle("drafts.create", func(r *http.Request) any {
		_ = r.ParseForm()
		d.mu.Lock()
		defer d.mu.Unlock()
		d.creates = append(d.creates, r.PostForm)
		if d.createErr != "" {
			return map[string]any{"ok": false, "error": d.createErr}
		}
		var blocks, dests any
		_ = json.Unmarshal([]byte(r.PostForm.Get("blocks")), &blocks)
		_ = json.Unmarshal([]byte(r.PostForm.Get("destinations")), &dests)
		sched := json.Number(r.PostForm.Get("date_scheduled"))
		return map[string]any{"ok": true, "draft": map[string]any{
			"id": "Dr0NEW", "date_scheduled": sched, "last_updated_ts": "1786752114.508819",
			"blocks": blocks, "destinations": dests,
		}}
	})
	srv.Handle("drafts.list", func(r *http.Request) any {
		d.mu.Lock()
		defer d.mu.Unlock()
		return map[string]any{"ok": true, "drafts": d.list, "has_more": d.hasMore}
	})
	srv.Handle("drafts.delete", func(r *http.Request) any {
		_ = r.ParseForm()
		d.mu.Lock()
		defer d.mu.Unlock()
		d.deletes = append(d.deletes, r.PostForm)
		if len(d.deleteErrs) > 0 {
			code := d.deleteErrs[0]
			d.deleteErrs = d.deleteErrs[1:]
			return map[string]any{"ok": false, "error": code}
		}
		return map[string]any{"ok": true}
	})
}

func richBlocks(text string) []any {
	return []any{map[string]any{"type": "rich_text", "elements": []any{
		map[string]any{"type": "rich_text_section", "elements": []any{map[string]any{"type": "text", "text": text}}},
	}}}
}

func draftItem(id, channel, thread string, scheduled int64, text string) map[string]any {
	dest := map[string]any{"channel_id": channel}
	if thread != "" {
		dest["thread_ts"] = thread
	}
	return map[string]any{
		"id": id, "date_scheduled": scheduled, "last_updated_ts": "1700000000.123456",
		"destinations": []any{dest}, "blocks": richBlocks(text),
	}
}

// scheduleSetup boots a provider whose account (U1) has a Denver profile
// zone on a Denver machine, with the clock at scheduleNow.
func scheduleSetup(t *testing.T) (*slacktest.Server, *provider.ApiProvider, *draftsFake) {
	t.Helper()
	srv := slacktest.New(t)
	srv.SeedUsers(
		slack.User{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie", TZ: "America/Denver"},
		slack.User{ID: "U2", Name: "schen", RealName: "Sarah Chen"},
	)
	d := &draftsFake{}
	d.install(srv)
	ap := bootedProvider(t, srv)
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatalf("zone: %v", err)
	}
	t.Cleanup(features.SetScheduleClockForTest(scheduleNow, denver))
	srv.ResetCalls()
	return srv, ap, d
}

func noWrites(t *testing.T, srv *slacktest.Server) {
	t.Helper()
	for _, m := range []string{"drafts.create", "drafts.delete", "chat.postMessage", "conversations.mark", "conversations.open"} {
		if n := srv.Calls(m); n != 0 {
			t.Fatalf("%s called %d times", m, n)
		}
	}
}

func TestSayAtSchedulesThroughDrafts(t *testing.T) {
	srv, ap, d := scheduleSetup(t)

	out := runTool(t, features.Say, ap, map[string]any{
		"to": "#eng", "text": "**hi**\n- one\n- two", "at": "2027-01-15T09:00",
	})

	if len(d.creates) != 1 {
		t.Fatalf("drafts.create calls: %d\n%s", len(d.creates), out)
	}
	form := d.creates[0]
	// 09:00 MST is 16:00 UTC.
	if want := time.Date(2027, 1, 15, 16, 0, 0, 0, time.UTC).Unix(); form.Get("date_scheduled") != strconv.FormatInt(want, 10) {
		t.Fatalf("date_scheduled %q, want %d", form.Get("date_scheduled"), want)
	}
	if form.Get("is_from_composer") != "true" || form.Get("file_ids") != "[]" {
		t.Fatalf("composer fields: %v", form)
	}
	if got := form.Get("destinations"); got != `[{"channel_id":"C1"}]` {
		t.Fatalf("destinations %s", got)
	}
	id := form.Get("client_msg_id")
	if len(id) != 36 || id != strings.ToLower(id) || id[14] != '4' {
		t.Fatalf("client_msg_id %q is not a lowercase UUIDv4", id)
	}
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(form.Get("blocks")), &blocks); err != nil || len(blocks) != 1 || blocks[0]["type"] != "rich_text" {
		t.Fatalf("blocks: %s", form.Get("blocks"))
	}
	if !strings.Contains(form.Get("blocks"), `"bold":true`) || !strings.Contains(form.Get("blocks"), "rich_text_list") {
		t.Fatalf("formatting lost: %s", form.Get("blocks"))
	}

	wantIn(t, out,
		"`say to='#eng' at=2027-01-15T09:00`",
		"Scheduled for Fri 2027-01-15 09:00 MST (UTC-07:00)", "16:00 UTC", "in 4h 0m", "to #eng",
		"sent from your account", "Cancel it: say cancel='ev_")
	if strings.Contains(out, "Dr0NEW") {
		t.Fatalf("draft ID leaked:\n%s", out)
	}
	for _, m := range []string{"chat.postMessage", "conversations.mark"} {
		if n := srv.Calls(m); n != 0 {
			t.Fatalf("%s called %d times", m, n)
		}
	}
}

func TestSayAtCarriesThreadAndBroadcast(t *testing.T) {
	_, ap, d := scheduleSetup(t)
	runTool(t, features.Say, ap, map[string]any{
		"to": "#eng", "text": "Reminder", "thread": "1782246118.543969", "broadcast": true,
		"at": "2027-01-15T14:00:00Z",
	})
	if len(d.creates) != 1 {
		t.Fatalf("drafts.create calls: %d", len(d.creates))
	}
	if got := d.creates[0].Get("destinations"); got != `[{"channel_id":"C1","thread_ts":"1782246118.543969","broadcast":true}]` {
		t.Fatalf("destinations %s", got)
	}
	if strings.Contains(d.creates[0].Get("destinations"), "reply_broadcast") {
		t.Fatalf("sent the key Slack drops")
	}
}

// Every refusal happens before any Slack write.
func TestSayAtRefusalsSendNothing(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"past", map[string]any{"to": "#eng", "text": "x", "at": "2027-01-15T11:00:00Z"}, "at least 2 minutes out"},
		{"too far", map[string]any{"to": "#eng", "text": "x", "at": "2027-06-01T09:00:00Z"}, "at most 120 days"},
		{"nonsense", map[string]any{"to": "#eng", "text": "x", "at": "soon"}, "Accepted:"},
		{"no text", map[string]any{"to": "#eng", "at": "2027-01-15T14:00:00Z"}, "at needs text"},
		{"emoji", map[string]any{"to": "#eng", "emoji": "tada", "messageTs": "1.1", "at": "2027-01-15T14:00:00Z"}, "can't be combined with emoji"},
		{"files", map[string]any{"to": "#eng", "text": "x", "files": []any{"a.pdf"}, "at": "2027-01-15T14:00:00Z"}, "at can't be combined with files"},
		{"cancel with text", map[string]any{"cancel": handle.Scheduled("C1", "Dr1"), "text": "x"}, "cancel= stands alone"},
		{"no destination", map[string]any{"text": "x", "at": "2027-01-15T14:00:00Z"}, "say needs to="},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, ap, _ := scheduleSetup(t)
			out := runTool(t, features.Say, ap, c.params)
			wantIn(t, out, c.want)
			noWrites(t, srv)
		})
	}
}

// A time with no offset is refused when the profile and machine zones
// disagree; the refusal names both and nothing is sent.
func TestSayAtRefusesANaiveTimeWhenZonesDisagree(t *testing.T) {
	srv, ap, d := scheduleSetup(t)
	restore := features.SetScheduleClockForTest(scheduleNow, time.UTC)
	defer restore()

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "text": "x", "at": "2027-01-15T09:00"})
	wantIn(t, out, "America/Denver", "UTC-07:00", "UTC+00:00", "Give an offset", "Nothing was scheduled")
	noWrites(t, srv)

	out = runTool(t, features.Say, ap, map[string]any{"to": "#eng", "text": "x", "at": "2027-01-15T09:00:00-07:00"})
	if len(d.creates) != 1 {
		t.Fatalf("explicit offset not scheduled:\n%s", out)
	}
	wantIn(t, out, "09:00 MST", "16:00 UTC on this machine")
}

func TestSayAtExplainsAttachedDraftExists(t *testing.T) {
	_, ap, d := scheduleSetup(t)
	d.createErr = "attached_draft_exists"
	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "text": "x", "at": "2027-01-15T14:00:00Z"})
	wantIn(t, out, "unsent draft in your composer", "Nothing was scheduled", "thread='<ts>'")
}

// The listing shows pending scheduled messages only, soonest first, by
// name, and never the person's composer drafts. It writes nothing.
func TestMessagesScheduledListsOnlyPendingScheduled(t *testing.T) {
	srv, ap, d := scheduleSetup(t)
	later := draftItem("Dr2", "C2", "", scheduleNow.Add(48*time.Hour).Unix(), "standup notes")
	sooner := draftItem("Dr1", "C1", "1782246118.543969", scheduleNow.Add(3*time.Hour).Unix(), "hi")
	typed := draftItem("Dr3", "C1", "", 0, "TYPED-COMPOSER-TEXT")
	sent := draftItem("Dr4", "C1", "", scheduleNow.Add(time.Hour).Unix(), "ALREADY-SENT")
	sent["is_sent"] = true
	d.list = []map[string]any{later, typed, sooner, sent}

	out := runTool(t, features.Messages, ap, map[string]any{"scheduled": true})
	wantIn(t, out, "`messages scheduled=true`", "2 scheduled messages, soonest first",
		"#eng (thread 1782246118.543969)", `"hi"`, "#platform", "scheduled from Slack")
	if strings.Index(out, `"hi"`) > strings.Index(out, "standup notes") {
		t.Fatalf("not soonest first:\n%s", out)
	}
	for _, leak := range []string{"TYPED-COMPOSER-TEXT", "ALREADY-SENT", "Dr1", "Dr2", "C1 ", "C2 "} {
		if strings.Contains(out, leak) {
			t.Fatalf("listing shows %q:\n%s", leak, out)
		}
	}
	if strings.Contains(out, "no next page") {
		t.Fatalf("cut-list line without has_more:\n%s", out)
	}
	noWrites(t, srv)

	narrowed := runTool(t, features.Messages, ap, map[string]any{"scheduled": true, "target": "#platform"})
	wantIn(t, narrowed, "1 scheduled message to #platform", "standup notes")
	if strings.Contains(narrowed, `"hi"`) {
		t.Fatalf("target did not narrow:\n%s", narrowed)
	}

	d.hasMore = true
	wantIn(t, runTool(t, features.Messages, ap, map[string]any{"scheduled": true}), "offers no next page")
	noWrites(t, srv)
}

func TestMessagesScheduledRunsInBatchWithoutWriting(t *testing.T) {
	srv, ap, d := scheduleSetup(t)
	d.list = []map[string]any{draftItem("Dr1", "C1", "", scheduleNow.Add(time.Hour).Unix(), "hi")}
	out := batchOut(t, ap, map[string]any{"commands": []any{
		map[string]any{"tool": "messages", "params": map[string]any{"scheduled": true}},
	}})
	wantIn(t, out, "1 scheduled message", `"hi"`)
	noWrites(t, srv)
}

// A message scheduled here and then edited in Slack lists as edited; the
// edit is reported, never blocked or undone.
func TestMessagesScheduledReportsAnEditMadeInSlack(t *testing.T) {
	srv, ap, d := scheduleSetup(t)
	runTool(t, features.Say, ap, map[string]any{"to": "#eng", "text": "hi", "at": "2027-01-15T14:00:00Z"})
	if len(d.creates) != 1 {
		t.Fatalf("not scheduled")
	}
	var dests, blocks any
	_ = json.Unmarshal([]byte(d.creates[0].Get("destinations")), &dests)
	_ = json.Unmarshal([]byte(d.creates[0].Get("blocks")), &blocks)
	asCreated := map[string]any{
		"id": "Dr0NEW", "date_scheduled": time.Date(2027, 1, 15, 14, 0, 0, 0, time.UTC).Unix(),
		"last_updated_ts": "1786752114.508819", "destinations": dests, "blocks": blocks,
	}

	d.list = []map[string]any{asCreated}
	wantIn(t, runTool(t, features.Messages, ap, map[string]any{"scheduled": true}), "scheduled here")

	edited := map[string]any{}
	for k, v := range asCreated {
		edited[k] = v
	}
	edited["last_updated_ts"] = "1786759999.000001"
	edited["date_scheduled"] = time.Date(2027, 1, 15, 15, 0, 0, 0, time.UTC).Unix()
	edited["blocks"] = richBlocks("hi, edited by hand")
	d.list = []map[string]any{edited}
	out := runTool(t, features.Messages, ap, map[string]any{"scheduled": true})
	wantIn(t, out, "edited in Slack since", "time moved from", "content changed", "hi, edited by hand")
	if n := srv.Calls("drafts.delete"); n != 0 {
		t.Fatalf("an edit was acted on: %d deletes", n)
	}
}

func TestSayCancelDeletesWithPaddedTimestampAndOneRetry(t *testing.T) {
	_, ap, d := scheduleSetup(t)
	d.list = []map[string]any{draftItem("Dr1", "C1", "", scheduleNow.Add(time.Hour).Unix(), "hi")}
	d.deleteErrs = []string{"draft_has_conflict"}

	out := runTool(t, features.Say, ap, map[string]any{"cancel": handle.Scheduled("C1", "Dr1")})
	if len(d.deletes) != 2 {
		t.Fatalf("drafts.delete calls: %d\n%s", len(d.deletes), out)
	}
	if got := d.deletes[0].Get("client_last_updated_ts"); got != "1700000000.1234560" {
		t.Fatalf("first attempt ts %q", got)
	}
	if d.deletes[0].Get("draft_id") != "Dr1" || d.deletes[1].Get("draft_id") != "Dr1" {
		t.Fatalf("deleted the wrong draft: %v", d.deletes)
	}
	if second := d.deletes[1].Get("client_last_updated_ts"); second <= "1700000000.1234560" || len(second) != len("1700000000.1234560") {
		t.Fatalf("retry ts %q is not a later 7-decimal time", second)
	}
	wantIn(t, out, "Cancelled the message scheduled for", "to #eng", "> hi")
}

// Cancel touches only a pending scheduled draft: a typed draft, a sent
// one, or a handle of another kind is refused with no delete.
func TestSayCancelRefusesAnythingButAPendingScheduledDraft(t *testing.T) {
	srv, ap, d := scheduleSetup(t)
	sent := draftItem("Dr4", "C1", "", scheduleNow.Add(time.Hour).Unix(), "x")
	sent["is_sent"] = true
	d.list = []map[string]any{draftItem("Dr3", "C1", "", 0, "typed"), sent}

	for _, h := range []string{handle.Scheduled("C1", "Dr3"), handle.Scheduled("C1", "Dr4"), handle.Scheduled("C1", "Dr9"), handle.Scheduled("C2", "Dr3"), handle.Message("C1", "1.1"), "nonsense"} {
		out := runTool(t, features.Say, ap, map[string]any{"cancel": h})
		wantIn(t, out, "Nothing was cancelled")
	}
	noWrites(t, srv)
}

// A scheduled send passes the approval gate with its time bound: an
// approval for one time does not cover another, nor an immediate send.
func TestScheduledSendApprovalBindsTheTime(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	d := &draftsFake{}
	d.install(f.srv)
	t.Cleanup(features.SetScheduleClockForTest(time.Now(), time.UTC))
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	other := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	call := func(when string) *features.FeatureResult {
		p := map[string]any{"to": "#partner", "text": "the roadmap"}
		if when != "" {
			p["at"] = when
		}
		return f.say(t, context.Background(), p)
	}

	first := call(at)
	wantIn(t, first.Message, "Needs operator approval: pending p", "message scheduled for", "Nothing was sent")
	ws := f.workspace(t)
	reqs := ws.Pending.List(time.Now())
	if len(reqs) != 1 || !strings.HasPrefix(reqs[0].Text, "[scheduled for ") {
		t.Fatalf("pending requests: %+v", reqs)
	}
	if _, err := ws.Approve(reqs[0], time.Now()); err != nil {
		t.Fatalf("approve: %v", err)
	}

	wantIn(t, call(other).Message, "Needs operator approval")
	wantIn(t, call("").Message, "Needs operator approval")
	if len(d.creates) != 0 || len(f.posted()) != 0 {
		t.Fatalf("an approval for one time let another send through")
	}
	if res := call(at); !res.Success {
		t.Fatalf("approved scheduled send refused: %+v", res)
	}
	if len(d.creates) != 1 || len(f.posted()) != 0 {
		t.Fatalf("creates=%d posts=%d", len(d.creates), len(f.posted()))
	}
}

// The scanner reads a scheduled send before the draft exists.
func TestScheduledSendIsScannedBeforeTheDraft(t *testing.T) {
	f := newSafetyFake(t, safety.Settings{Identity: safety.Human, Posture: safety.Soft})
	d := &draftsFake{}
	d.install(f.srv)
	t.Cleanup(features.SetScheduleClockForTest(time.Now(), time.UTC))
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	res := f.say(t, context.Background(), map[string]any{"to": "#eng", "text": "token " + fakeToken(), "at": at})
	if res.Success || len(d.creates) != 0 {
		t.Fatalf("a secret was scheduled: %+v creates=%d", res, len(d.creates))
	}
}
