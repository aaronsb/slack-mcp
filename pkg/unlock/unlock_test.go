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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/safety"
)

var org = safety.Org{TeamID: "T1", UserID: "U0"}

// locked is a strict workspace after two blocks: #a (C1) and @dana (U1,
// DM D1) quarantined, and the strike lock engaged.
func locked(t *testing.T) *safety.Workspace {
	t.Helper()
	isolate(t)
	ws, err := safety.OpenDir(t.TempDir(), org, safety.Strict)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []safety.Destination{
		{Kind: safety.DestChannel, ConversationID: "C1", Name: "#a"},
		{Kind: safety.DestDM, ConversationID: "D1", Name: "@dana", Members: []safety.Key{safety.Person("U1", "@dana")}},
	} {
		if _, err := ws.Quarantine.RecordBlock(safety.Block{
			Destination: d,
			Call:        safety.CallRecord{Tool: "say", Destination: d.Name},
			Match:       safety.MatchRecord{Class: "aws-access-key", Location: "text"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

// isolate points every XDG directory at the test's own temp dir.
func isolate(t *testing.T) {
	t.Helper()
	for _, v := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(v, t.TempDir())
	}
}

func ephemeral() (int, net.Listener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}
	return l.Addr().(*net.TCPAddr).Port, l, nil
}

func start(t *testing.T, s Store) *Instance {
	t.Helper()
	in, err := Start(s, Options{
		Listen:   ephemeral,
		Describe: func(r safety.Reason) string { return r.Class + " in " + r.Location + " to " + r.Destination },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(in.Stop)
	return in
}

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

func post(t *testing.T, in *Instance, origin string, form url.Values) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, in.URL(), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func self(in *Instance) string { return "http://127.0.0.1:" + strconv.Itoa(in.Port()) }

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

// Clear applies only the checked rows, marks them by=web, and stops the
// instance; the same link then fails.
func TestClearAppliesCheckedRowsAndCloses(t *testing.T) {
	ws := locked(t)
	in := start(t, ws.Quarantine)
	get(t, in.URL()) // rows: 0 the lock, 1 @dana, 2 #a

	code, body := post(t, in, self(in), url.Values{"action": {"clear"}, "row": {"1"}})
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

func TestDoneClosesWithoutClearing(t *testing.T) {
	ws := locked(t)
	in := start(t, ws.Quarantine)
	get(t, in.URL())
	code, body := post(t, in, self(in), url.Values{"action": {"done"}, "row": {"0", "1", "2"}})
	if code != http.StatusOK || !strings.Contains(body, "without clearing") {
		t.Fatalf("POST %d: %s", code, body)
	}
	waitDone(t, in)
	if st := ws.Quarantine.State(); !st.LockEngaged() || len(st.People) != 1 || len(st.Conversations) != 1 {
		t.Fatalf("Done cleared something: %+v", st)
	}
}

// A POST from another origin, or with none, is refused and leaves the page
// open; a GET never clears; other paths are not found.
func TestRefusesForeignPostsAndOtherPaths(t *testing.T) {
	ws := locked(t)
	in := start(t, ws.Quarantine)
	get(t, in.URL())
	for _, origin := range []string{"", "http://evil.example", "null"} {
		if code, _ := post(t, in, origin, url.Values{"action": {"clear"}, "row": {"0"}}); code != http.StatusForbidden {
			t.Fatalf("origin %q: %d, want 403", origin, code)
		}
	}
	if code, _ := get(t, in.URL()+"?action=clear&row=0"); code != http.StatusOK {
		t.Fatalf("GET after refusals: %d", code)
	}
	if code, _ := get(t, self(in)+"/unlock/wrong"); code != http.StatusNotFound {
		t.Fatalf("wrong token: %d", code)
	}
	if !ws.Quarantine.State().LockEngaged() {
		t.Fatal("a refused request cleared the lock")
	}
}

func TestIdleInstanceStops(t *testing.T) {
	in, err := Start(locked(t).Quarantine, Options{Listen: ephemeral, Idle: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, in)
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

// The in-memory hold after an unrecorded block has no strikes in the file;
// the page still offers the strike row, and clearing it writes the clear
// that releases the hold.
func TestHeldWritesOfferTheStrikeRow(t *testing.T) {
	isolate(t)
	ws, err := safety.OpenDir(t.TempDir(), org, safety.Strict)
	if err != nil {
		t.Fatal(err)
	}
	in, err := Start(ws.Quarantine, Options{Listen: ephemeral, Held: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(in.Stop)
	_, body := get(t, in.URL())
	if !strings.Contains(body, "Writes held") || !strings.Contains(body, `value="clear"`) {
		t.Fatalf("page: %s", body)
	}
	before := ws.Quarantine.State().Position
	if code, b := post(t, in, self(in), url.Values{"action": {"clear"}, "row": {"0"}}); code != http.StatusOK || !strings.Contains(b, "Cleared 1.") {
		t.Fatalf("POST %d: %s", code, b)
	}
	if !ws.Quarantine.State().ClearedStrikesAfter(before) {
		t.Fatal("no clear of strikes written after the hold")
	}
}
