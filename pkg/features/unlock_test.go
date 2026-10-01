package features_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/safety"
	"github.com/aaronsb/slack-mcp/pkg/setup"
)

// unlockToken matches the page's secret path; no tool result may carry it.
var unlockToken = regexp.MustCompile(`/unlock/[0-9a-f]{32}`)

// openedPage stands in for the browser: it records the URL the tool
// launched, which only the person at the browser would see.
func openedPage(t *testing.T) *string {
	t.Helper()
	var got string
	restore := features.SetOpenBrowserForTest(func(u string) error {
		got = u
		return nil
	})
	t.Cleanup(restore)
	return &got
}

func callUnlock(t *testing.T, ctx context.Context, f *safetyFake) *features.FeatureResult {
	t.Helper()
	res, err := features.Unlock.Handler(ctx, map[string]any{"_provider": f.ap})
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	for _, s := range []string{res.Message, res.Guidance} {
		if unlockToken.MatchString(s) || strings.Contains(s, "127.0.0.1") {
			t.Fatalf("result carries the page's address: %s", s)
		}
	}
	return res
}

func fetch(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// After a block, a refusal offers unlock; unlock opens the page in the
// browser and its result carries neither the link nor the state; the page
// names the quarantine by name, and Clear there lifts it.
func TestUnlockOpensAPageTheOperatorClearsOn(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	tok := fakeToken()
	f.say(t, context.Background(), map[string]any{"to": "#eng", "text": "token " + tok})
	again := f.say(t, context.Background(), map[string]any{"to": "#eng", "text": "harmless"})
	wantIn(t, again.Guidance, "call unlock")

	link := openedPage(t)
	res := callUnlock(t, context.Background(), f)
	if !res.Success || *link == "" {
		t.Fatalf("page not opened: %+v", res)
	}
	if strings.Contains(res.Message, "#eng") || strings.Contains(strings.ToLower(res.Message+res.Guidance), "cleared ") {
		t.Fatalf("result reports the state or an outcome: %+v", res)
	}

	_, body := fetch(t, *link)
	wantIn(t, body, "#eng", "A Slack token in the message text, sending to #eng")
	if strings.Contains(body, "C1") || strings.Contains(body, tok) {
		t.Fatalf("page shows an ID or the value:\n%s", body)
	}

	u, _ := url.Parse(*link)
	at := regexp.MustCompile(`name="at" value="([^"]*)"`).FindStringSubmatch(body)[1]
	row := regexp.MustCompile(`name="row" value="([^"]*)"><span><span class="title">#eng<`).FindStringSubmatch(body)[1]
	req, _ := http.NewRequest(http.MethodPost, *link, strings.NewReader(url.Values{"action": {"clear"}, "at": {at}, "row": {row}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if st := f.workspace(t).Quarantine.State(); len(st.Conversations) != 0 || st.Strikes != 1 {
		t.Fatalf("after clearing #eng: %+v", st)
	}
}

// With no browser to open (suppressed, or the launch failed), unlock
// refuses without a link, points at the CLI, and leaves no page running.
func TestUnlockWithoutABrowserRefusesWithoutALink(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	var opened string
	restore := features.SetOpenBrowserForTest(func(u string) error {
		opened = u
		return errors.New("no browser")
	})
	t.Cleanup(restore)
	res := callUnlock(t, context.Background(), f)
	if res.Success {
		t.Fatalf("succeeded without a browser: %+v", res)
	}
	wantIn(t, res.Message, "slack-mcp quarantine")
	if code, _ := fetch(t, opened); code == http.StatusOK {
		t.Fatal("a page nobody can open is still serving")
	}
}

// A second unlock stops the first page.
func TestUnlockReplacesTheEarlierPage(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	link := openedPage(t)
	callUnlock(t, context.Background(), f)
	first := *link
	callUnlock(t, context.Background(), f)
	second := *link
	if first == "" || second == "" || first == second {
		t.Fatalf("links: %q %q", first, second)
	}
	if code, _ := fetch(t, first); code == http.StatusOK {
		t.Fatal("the earlier page still answers")
	}
	if code, _ := fetch(t, second); code != http.StatusOK {
		t.Fatalf("the new page does not answer: %d", code)
	}
}

func TestUnlockRefusedOnRemoteDeployment(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	link := openedPage(t)
	t.Setenv("SLACK_MCP_DEPLOYMENT", "remote")
	res := callUnlock(t, context.Background(), f)
	if res.Success || *link != "" {
		t.Fatalf("remote unlock opened a page: %+v", res)
	}
	wantIn(t, res.Message, "remote deployment")
}

// On SSE the server's host need not be the operator's, as with elicitation.
func TestUnlockRefusedOnSSE(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	link := openedPage(t)
	res := callUnlock(t, features.WithSSE(context.Background(), true), f)
	if res.Success || *link != "" {
		t.Fatalf("SSE unlock opened a page: %+v", res)
	}
	wantIn(t, res.Message, "SSE", "slack-mcp quarantine")
}

func TestInstructionsOfferUnlock(t *testing.T) {
	got := features.Instructions(safety.Org{}, "")
	wantIn(t, got, "call unlock")
	if strings.Contains(got, "link") {
		t.Fatalf("instructions promise a link: %s", got)
	}
}

// The setup page's launch reports suppression to its caller rather than
// writing anything itself; on stdio, stdout is the JSON-RPC stream.
func TestOpenBrowserURLReportsSuppression(t *testing.T) {
	t.Setenv(setup.NoBrowserEnv, "1")
	if err := setup.OpenBrowserURL("http://127.0.0.1:1/x"); err == nil {
		t.Fatal("a suppressed launch reported success")
	}
}
