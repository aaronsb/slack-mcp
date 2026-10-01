package unlock

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/safety"
)

var org = safety.Org{TeamID: "T1", UserID: "U0"}

// isolate points every XDG directory at the test's own temp dir.
func isolate(t *testing.T) {
	t.Helper()
	for _, v := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(v, t.TempDir())
	}
}

func blockOn(t *testing.T, ws *safety.Workspace, d safety.Destination) {
	t.Helper()
	if _, err := ws.Quarantine.RecordBlock(safety.Block{
		Destination: d,
		Call:        safety.CallRecord{Tool: "say", Destination: d.Name},
		Match:       safety.MatchRecord{Class: "aws-access-key", Location: "text"},
	}); err != nil {
		t.Fatal(err)
	}
}

func chanDest(id, name string) safety.Destination {
	return safety.Destination{Kind: safety.DestChannel, ConversationID: id, Name: name}
}

// locked is a strict workspace after two blocks: #a (C1) and @dana (U1,
// DM D1) quarantined, and the strike lock engaged.
func locked(t *testing.T) *safety.Workspace {
	t.Helper()
	isolate(t)
	ws, err := safety.OpenDir(t.TempDir(), org, safety.Strict)
	if err != nil {
		t.Fatal(err)
	}
	blockOn(t, ws, chanDest("C1", "#a"))
	blockOn(t, ws, safety.Destination{Kind: safety.DestDM, ConversationID: "D1", Name: "@dana", Members: []safety.Key{safety.Person("U1", "@dana")}})
	return ws
}

func ephemeral() (int, net.Listener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}
	return l.Addr().(*net.TCPAddr).Port, l, nil
}

func startWith(t *testing.T, s Store, opts Options) *Instance {
	t.Helper()
	opts.Listen = ephemeral
	if opts.Describe == nil {
		opts.Describe = func(r safety.Reason) string { return r.Class + " in " + r.Location + " to " + r.Destination }
	}
	in, err := Start(s, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(in.Stop)
	return in
}

func start(t *testing.T, s Store) *Instance { return startWith(t, s, Options{}) }

func get(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func postWith(t *testing.T, in *Instance, header map[string]string, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, in.URL(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func post(t *testing.T, in *Instance, origin string, form url.Values) (int, string) {
	t.Helper()
	h := map[string]string{}
	if origin != "" {
		h["Origin"] = origin
	}
	return postWith(t, in, h, form.Encode())
}

func self(in *Instance) string { return "http://127.0.0.1:" + strconv.Itoa(in.Port()) }

var (
	rowRE = regexp.MustCompile(`name="row" value="([^"]*)"><span><span class="title">([^<]*)<`)
	atRE  = regexp.MustCompile(`name="at" value="([^"]*)"`)
)

// form builds a Clear or Done answer to a rendered page, checking the rows
// whose titles start with any of titles.
func form(body, action string, titles ...string) url.Values {
	v := url.Values{"action": {action}}
	if m := atRE.FindStringSubmatch(body); m != nil {
		v.Set("at", m[1])
	}
	for _, m := range rowRE.FindAllStringSubmatch(body, -1) {
		for _, title := range titles {
			if strings.HasPrefix(m[2], title) {
				v.Add("row", m[1])
			}
		}
	}
	return v
}

func waitDone(t *testing.T, in *Instance) {
	t.Helper()
	select {
	case <-in.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("instance did not stop")
	}
}

func TestPageListsLocksWithoutIDs(t *testing.T) {
	in := start(t, locked(t).Quarantine)
	code, body := get(t, in.URL())
	if code != http.StatusOK {
		t.Fatalf("GET %d: %s", code, body)
	}
	for _, want := range []string{"Strike lock engaged (2 of 2)", "@dana", "#a", "aws-access-key in text to #a", "aws-access-key in text to @dana", `value="clear"`, `value="done"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, id := range []string{"C1", "D1", "U1", "T1"} {
		if strings.Contains(body, id) {
			t.Errorf("page shows the ID %s", id)
		}
	}
}

// Under Referrer-Policy: no-referrer, Chromium and Firefox send
// "Origin: null" on the form POST, which the check refuses; same-origin
// keeps the real origin.
func TestReferrerPolicyKeepsTheFormsOrigin(t *testing.T) {
	in := start(t, locked(t).Quarantine)
	resp, err := http.Get(in.URL())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("Referrer-Policy %q, want same-origin", got)
	}
}

// Clear applies only the checked rows, marks them by=web, and stops the
// instance; the same link then fails.
func TestClearAppliesCheckedRowsAndCloses(t *testing.T) {
	ws := locked(t)
	in := start(t, ws.Quarantine)
	_, page := get(t, in.URL())

	code, body := post(t, in, self(in), form(page, "clear", "@dana"))
	if code != http.StatusOK || !strings.Contains(body, "Cleared 1.") {
		t.Fatalf("POST %d: %s", code, body)
	}
	waitDone(t, in)

	st := ws.Quarantine.State()
	if _, ok := st.IsQuarantined("U1"); ok {
		t.Fatal("checked @dana still quarantined")
	}
	if _, ok := st.IsQuarantined("C1"); !ok || !st.LockEngaged() {
		t.Fatal("an unchecked row was cleared")
	}
	raw, _ := os.ReadFile(filepath.Join(ws.Dir, safety.QuarantineFile))
	if bytes.Count(raw, []byte(`"by":"web"`)) != 1 {
		t.Fatalf("want one web clear:\n%s", raw)
	}

	if code, _ := get(t, in.URL()); code == http.StatusOK {
		t.Fatal("the link still answers after Clear")
	}
}

// A block between loading the page and pressing Clear changes the rows:
// the answer to the old page applies nothing and shows the new state.
func TestClearAfterTheLocksChangedAppliesNothing(t *testing.T) {
	ws := locked(t)
	in := start(t, ws.Quarantine)
	_, first := get(t, in.URL())
	blockOn(t, ws, chanDest("C0", "#0new")) // sorts before #a
	get(t, in.URL())

	code, body := post(t, in, self(in), form(first, "clear", "#a"))
	if code != http.StatusOK || !strings.Contains(body, "The locks changed") {
		t.Fatalf("POST %d: %s", code, body)
	}
	st := ws.Quarantine.State()
	if _, ok := st.IsQuarantined("C0"); !ok {
		t.Fatal("the new block's quarantine was cleared by an answer to the old page")
	}
	if _, ok := st.IsQuarantined("C1"); !ok {
		t.Fatal("a changed page still applied its answer")
	}
	select {
	case <-in.Done():
		t.Fatal("the page closed without applying anything")
	default:
	}
	// Answering the page as it now stands works.
	if code, body := post(t, in, self(in), form(body, "clear", "#0new")); code != http.StatusOK || !strings.Contains(body, "Cleared 1.") {
		t.Fatalf("second POST %d: %s", code, body)
	}
	if _, ok := ws.Quarantine.State().IsQuarantined("C0"); ok {
		t.Fatal("#0new not cleared on the current page")
	}
}

func TestDoneClosesWithoutClearing(t *testing.T) {
	ws := locked(t)
	in := start(t, ws.Quarantine)
	_, page := get(t, in.URL())
	code, body := post(t, in, self(in), form(page, "done", "Strike", "@dana", "#a"))
	if code != http.StatusOK || !strings.Contains(body, "without clearing") {
		t.Fatalf("POST %d: %s", code, body)
	}
	waitDone(t, in)
	if st := ws.Quarantine.State(); !st.LockEngaged() || len(st.People) != 1 || len(st.Conversations) != 1 {
		t.Fatalf("Done cleared something: %+v", st)
	}
}

// A POST from another origin, with none, with Origin null, or marked
// cross-site by Fetch Metadata is refused and leaves the page open; a GET
// never clears; other paths are not found.
func TestRefusesForeignPostsAndOtherPaths(t *testing.T) {
	ws := locked(t)
	in := start(t, ws.Quarantine)
	_, page := get(t, in.URL())
	body := form(page, "clear", "Strike").Encode()
	for _, h := range []map[string]string{
		{},
		{"Origin": "http://evil.example"},
		{"Origin": "null"},
		{"Origin": self(in), "Sec-Fetch-Site": "cross-site"},
		{"Origin": self(in), "Sec-Fetch-Site": "same-site"},
	} {
		if code, _ := postWith(t, in, h, body); code != http.StatusForbidden {
			t.Fatalf("headers %v: %d, want 403", h, code)
		}
	}
	if code, _ := get(t, in.URL()+"?action=clear&row=x"); code != http.StatusOK {
		t.Fatalf("GET after refusals: %d", code)
	}
	if code, _ := get(t, self(in)+"/unlock/wrong"); code != http.StatusNotFound {
		t.Fatalf("wrong token: %d", code)
	}
	if !ws.Quarantine.State().LockEngaged() {
		t.Fatal("a refused request cleared the lock")
	}
	if code, b := postWith(t, in, map[string]string{"Origin": self(in), "Sec-Fetch-Site": "same-origin"}, body); code != http.StatusOK || !strings.Contains(b, "Cleared 1.") {
		t.Fatalf("same-origin POST %d: %s", code, b)
	}
}

// A body far larger than the form is refused before anything is applied,
// and the page stays open.
func TestOversizeFormRefused(t *testing.T) {
	ws := locked(t)
	in := start(t, ws.Quarantine)
	get(t, in.URL())
	big := "action=done&pad=" + strings.Repeat("x", 64<<10)
	if code, _ := postWith(t, in, map[string]string{"Origin": self(in)}, big); code == http.StatusOK {
		t.Fatal("oversize form accepted")
	}
	select {
	case <-in.Done():
		t.Fatal("an oversize form closed the page")
	default:
	}
}

// The idle timeout counts from the last load, not from the start.
func TestLoadRestartsTheIdleTimer(t *testing.T) {
	in := startWith(t, locked(t).Quarantine, Options{Idle: 400 * time.Millisecond})
	time.Sleep(250 * time.Millisecond)
	get(t, in.URL())
	time.Sleep(300 * time.Millisecond) // 550ms from start, 300ms from the load
	select {
	case <-in.Done():
		t.Fatal("stopped while the operator had the page loaded")
	default:
	}
	waitDone(t, in)
}

func TestIdleInstanceStops(t *testing.T) {
	in, err := Start(locked(t).Quarantine, Options{Listen: ephemeral, Idle: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, in)
}

// Two quarantines the cache cannot name render with the same name; the
// page tells them apart.
func TestUnnamedRowsAreToldApart(t *testing.T) {
	ws := locked(t)
	blockOn(t, ws, chanDest("C7", "#x"))
	in := startWith(t, ws.Quarantine, Options{Name: func(k safety.Key) string {
		if k.Kind == safety.KeyConversation {
			return "a conversation"
		}
		return k.Name
	}})
	_, body := get(t, in.URL())
	titles := map[string]bool{}
	for _, m := range rowRE.FindAllStringSubmatch(body, -1) {
		if titles[m[2]] {
			t.Fatalf("two rows titled %q:\n%s", m[2], body)
		}
		titles[m[2]] = true
	}
}

// The in-memory hold after an unrecorded block has no strikes in the file;
// the page still offers the strike row, and clearing it writes the clear
// that releases the hold.
func TestHeldWritesOfferTheStrikeRow(t *testing.T) {
	isolate(t)
	ws, err := safety.OpenDir(t.TempDir(), org, safety.Strict)
	if err != nil {
		t.Fatal(err)
	}
	in := startWith(t, ws.Quarantine, Options{Held: func() bool { return true }})
	_, body := get(t, in.URL())
	if !strings.Contains(body, "Writes held") || !strings.Contains(body, `value="clear"`) {
		t.Fatalf("page: %s", body)
	}
	before := ws.Quarantine.State().Position
	if code, b := post(t, in, self(in), form(body, "clear", "Writes held")); code != http.StatusOK || !strings.Contains(b, "Cleared 1.") {
		t.Fatalf("POST %d: %s", code, b)
	}
	if !ws.Quarantine.State().ClearedStrikesAfter(before) {
		t.Fatal("no clear of strikes written after the hold")
	}
}

type unreadable struct{}

func (unreadable) State() safety.QuarantineState {
	return safety.QuarantineState{Err: errors.New("permission denied")}
}
func (unreadable) Clear(safety.Key, string, string, time.Time) (bool, error) {
	return false, errors.New("unreachable")
}

func TestUnreadableStateOffersOnlyDone(t *testing.T) {
	isolate(t)
	in := start(t, unreadable{})
	_, body := get(t, in.URL())
	if !strings.Contains(body, "cannot be read") || strings.Contains(body, `value="clear"`) {
		t.Fatalf("page: %s", body)
	}
}
