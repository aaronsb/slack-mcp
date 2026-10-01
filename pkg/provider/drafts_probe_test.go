//go:build probe

// Drafts API probe for scheduled send (#101). NOT part of `make test` or
// `make check`: the build tag keeps it out of every normal build, and it
// talks to a real workspace.
//
// What it answers, before scheduled send is designed on it:
//   - Does drafts.create accept our session tokens through the internal
//     client's form POST, with blocks built by text.ToRichText?
//   - Does it accept slack-go's rich_text JSON as-is, including the
//     "border":0 / "offset":0 keys RichTextList always emits (the Python
//     original never sends them)? The probe message includes a list so the
//     keys are on the wire.
//   - What shape do drafts.create and drafts.list return, and does
//     drafts.delete want client_last_updated_ts padded to 7 decimals
//     (falling back to the current time on draft_has_conflict)?
//
// It works only in your own self-DM: it creates one draft scheduled an hour
// out, finds it in drafts.list, and deletes it. Nothing is posted. If the
// delete fails, the draft would send to your self-DM in an hour — the test
// logs its id; cancel it from Slack's "Scheduled" list. It never calls
// conversations.mark. The provider cache goes to a temp dir, not your real
// XDG data directory.
//
// Run it manually, with real tokens in the environment (the config file is
// not read):
//
//	SLACK_MCP_XOXC_TOKEN=xoxc-... SLACK_MCP_XOXD_TOKEN=xoxd-... \
//	  go test -tags probe -run TestProbeDraftsLifecycle -v -count=1 ./pkg/provider/
//
// The -v output logs every raw response; paste it into #101.
package provider_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/text"
	"github.com/slack-go/slack"
)

type draftResponse struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Draft json.RawMessage `json:"draft"`
}

type draftListResponse struct {
	OK     bool              `json:"ok"`
	Error  string            `json:"error"`
	Drafts []json.RawMessage `json:"drafts"`
}

type draftRef struct {
	ID            string `json:"id"`
	LastUpdatedTS string `json:"last_updated_ts"`
	DateScheduled int64  `json:"date_scheduled"`
}

func uuid4(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// padTS pads a Slack timestamp's fraction to 7 digits, which drafts.delete
// wants instead of the 6 the API returns.
func padTS(ts string) string {
	whole, frac, _ := strings.Cut(ts, ".")
	for len(frac) < 7 {
		frac += "0"
	}
	return whole + "." + frac
}

func TestProbeDraftsLifecycle(t *testing.T) {
	xoxc, xoxd := os.Getenv("SLACK_MCP_XOXC_TOKEN"), os.Getenv("SLACK_MCP_XOXD_TOKEN")
	if xoxc == "" || xoxd == "" {
		t.Skip("set SLACK_MCP_XOXC_TOKEN and SLACK_MCP_XOXD_TOKEN to run the drafts probe")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	ap := provider.NewWithTokens(xoxc, xoxd)
	api, err := ap.Provide()
	if err != nil {
		t.Fatalf("Provide: %v", err)
	}
	me, err := api.AuthTestContext(ctx)
	if err != nil {
		t.Fatalf("auth.test: %v", err)
	}
	selfDM, err := ap.OpenDM(ctx, me.UserID)
	if err != nil {
		t.Fatalf("open self-DM: %v", err)
	}
	ic := ap.ProvideInternalClient()

	blocks, err := json.Marshal([]slack.Block{text.ToRichText(
		"*drafts probe* for #101 — safe to delete\n- list item (carries border/offset)\n  - nested")})
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	destinations, _ := json.Marshal([]map[string]string{{"channel_id": selfDM}})
	t.Logf("blocks sent: %s", blocks)

	// 1. drafts.create, scheduled an hour out.
	var created draftResponse
	err = ic.PostFormInternalAPI(ctx, "/api/drafts.create", url.Values{
		"blocks":           {string(blocks)},
		"destinations":     {string(destinations)},
		"client_msg_id":    {uuid4(t)},
		"file_ids":         {"[]"},
		"is_from_composer": {"true"},
		"date_scheduled":   {fmt.Sprint(time.Now().Add(time.Hour).Unix())},
	}, &created)
	if err != nil {
		t.Fatalf("drafts.create transport: %v", err)
	}
	t.Logf("drafts.create: ok=%v error=%q draft=%s", created.OK, created.Error, created.Draft)
	if !created.OK {
		t.Fatalf("drafts.create rejected: %s", created.Error)
	}
	var draft draftRef
	if err := json.Unmarshal(created.Draft, &draft); err != nil || draft.ID == "" {
		t.Fatalf("draft id not found in response (%v)", err)
	}
	t.Logf("created draft %s (last_updated_ts=%s, date_scheduled=%d) — cancel it from Slack's Scheduled list if this run fails",
		draft.ID, draft.LastUpdatedTS, draft.DateScheduled)

	// 2. drafts.list must show it.
	var listed draftListResponse
	if err := ic.PostFormInternalAPI(ctx, "/api/drafts.list",
		url.Values{"is_active": {"true"}, "limit": {"100"}}, &listed); err != nil {
		t.Fatalf("drafts.list transport: %v", err)
	}
	found := false
	for _, raw := range listed.Drafts {
		var d draftRef
		if json.Unmarshal(raw, &d) == nil && d.ID == draft.ID {
			found = true
			t.Logf("drafts.list entry: %s", raw)
			if d.LastUpdatedTS != "" {
				draft.LastUpdatedTS = d.LastUpdatedTS
			}
		}
	}
	t.Logf("drafts.list: ok=%v error=%q count=%d found=%v", listed.OK, listed.Error, len(listed.Drafts), found)
	if !found {
		t.Errorf("created draft missing from drafts.list")
	}

	// 3. drafts.delete: padded timestamp first, current time on conflict.
	del := func(ts string) draftResponse {
		var res draftResponse
		if err := ic.PostFormInternalAPI(ctx, "/api/drafts.delete",
			url.Values{"draft_id": {draft.ID}, "client_last_updated_ts": {ts}}, &res); err != nil {
			t.Fatalf("drafts.delete transport: %v", err)
		}
		t.Logf("drafts.delete(client_last_updated_ts=%s): ok=%v error=%q", ts, res.OK, res.Error)
		return res
	}
	res := del(padTS(draft.LastUpdatedTS))
	if !res.OK && res.Error == "draft_has_conflict" {
		res = del(fmt.Sprintf("%.7f", float64(time.Now().UnixNano())/1e9))
	}
	if !res.OK {
		t.Fatalf("drafts.delete failed (%s): draft %s is still scheduled — cancel it in Slack", res.Error, draft.ID)
	}
}
