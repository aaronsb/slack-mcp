// Package unlock is ADR-013's local clearing page: a one-shot web page on
// the loopback address where the person at the keyboard reviews the
// quarantines and the strike lock and clears what they choose. The page
// clears; the tool that opens it does not.
package unlock

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/safety"
	"github.com/aaronsb/slack-mcp/pkg/setup"
)

//go:embed page.html
var pageHTML string

var page = template.Must(template.New("unlock").Parse(pageHTML))

// IdleTimeout stops an instance nobody has loaded or answered for that
// long; each load restarts it.
const IdleTimeout = 15 * time.Minute

// maxForm bounds a POST body; the form holds a few row IDs.
const maxForm = 8 << 10

// ReferrerPolicy keeps the form POST's Origin header: under no-referrer
// Chromium and Firefox send "Origin: null", which the check refuses.
const ReferrerPolicy = "same-origin"

// Store is the quarantine state the page reads and clears. ClearKeysAt
// applies nothing when the file has moved past the place the page read.
type Store interface {
	State() safety.QuarantineState
	ClearKeysAt(targets []safety.Key, by string, at safety.Place, now time.Time) (moved bool, cleared []bool, err error)
}

// Options are what the caller supplies: how to name a key and describe a
// reason for a person, never by ID.
type Options struct {
	Name     func(safety.Key) string
	Describe func(safety.Reason) string
	// Held reports the server's in-memory hold after a block the file
	// could not record; the strike row then shows even with no strikes.
	Held func() bool
	Now  func() time.Time
	Idle time.Duration
	// Listen returns the port and listener; setup.FindPort by default.
	Listen func() (int, net.Listener, error)
}

// Instance is one running page. It serves one answer, Clear or Done, and
// stops.
type Instance struct {
	store Store
	opts  Options
	token string
	// secret keys the row IDs, so a row's ID names its key and nothing
	// else, whatever order the rows render in.
	secret []byte
	local  *setup.LocalServer

	mu     sync.Mutex
	closed bool
	done   chan struct{}
	timer  *time.Timer
}

type row struct {
	Key     safety.Key
	ID      string
	Title   string
	Scope   string
	Reasons []string
}

// Start serves a new page and returns it. The caller opens URL.
func Start(store Store, opts Options) (*Instance, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Idle == 0 {
		opts.Idle = IdleTimeout
	}
	if opts.Listen == nil {
		opts.Listen = setup.FindPort
	}
	if opts.Name == nil {
		opts.Name = func(k safety.Key) string { return k.Name }
	}
	if opts.Held == nil {
		opts.Held = func() bool { return false }
	}
	if opts.Describe == nil {
		opts.Describe = func(r safety.Reason) string { return r.Class }
	}
	var b [48]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	port, listener, err := opts.Listen()
	if err != nil {
		return nil, err
	}
	in := &Instance{store: store, opts: opts, token: hex.EncodeToString(b[:16]), secret: b[16:], done: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/unlock/"+in.token, in.serve)
	in.local = setup.NewLocalServer(listener, port, mux)
	in.mu.Lock()
	in.timer = time.AfterFunc(opts.Idle, in.Stop)
	in.mu.Unlock()
	in.local.Start()
	return in, nil
}

// URL is the page's link, token included.
func (in *Instance) URL() string { return in.local.BaseURL() + "/unlock/" + in.token }

// Port is the loopback port the page listens on.
func (in *Instance) Port() int { return in.local.Port() }

// Done is closed when the instance stops.
func (in *Instance) Done() <-chan struct{} { return in.done }

// Stop closes the page; later requests fail. Safe to call more than once.
func (in *Instance) Stop() {
	in.mu.Lock()
	first := !in.closed
	in.closed = true
	in.timer.Stop()
	in.mu.Unlock()
	if first {
		close(in.done)
	}
	go in.local.Stop()
}

type view struct {
	Unreadable string
	Lock       *row
	Rows       []row
	Malformed  int
	// At is the state the rows were read from; a Clear carries it back,
	// and one answering a different state applies nothing (Changed).
	At      string
	Changed bool
	Result  string

	place safety.Place
}

func (in *Instance) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", ReferrerPolicy)

	var form map[string][]string
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		if !setup.IsLoopbackOrigin(r.Header.Get("Origin"), in.local.Port()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// Read the body before taking the lock, so a slow or large
		// sender cannot hold the page.
		r.Body = http.MaxBytesReader(w, r.Body, maxForm)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "the form is too large or malformed", http.StatusRequestEntityTooLarge)
			return
		}
		form = r.PostForm
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	in.mu.Lock()
	defer in.mu.Unlock()
	if in.closed {
		http.Error(w, "This page is closed. Ask for a new one to clear anything else.", http.StatusGone)
		return
	}
	cur := in.list()
	if form == nil || (first(form["action"]) == "clear" && first(form["at"]) != cur.At) {
		// A load, or a Clear answering a page whose locks have changed
		// since: apply nothing and show the current state.
		cur.Changed = form != nil
		in.timer.Reset(in.opts.Idle)
		in.render(w, cur)
		return
	}
	result, moved := in.answer(form, cur)
	if moved {
		// A block or clear landed between the check above and the write.
		cur = in.list()
		cur.Changed = true
		in.timer.Reset(in.opts.Idle)
		in.render(w, cur)
		return
	}
	v := view{Result: result}
	in.closed = true
	in.timer.Stop()
	close(in.done)
	in.render(w, v)
	go in.local.Stop()
}

// rowID names a key on the page: an HMAC under the instance's secret, so
// it is stable across loads and reveals nothing about the key.
func (in *Instance) rowID(k safety.Key) string {
	m := hmac.New(sha256.New, in.secret)
	m.Write([]byte(string(k.Kind) + "\x00" + k.ID))
	return hex.EncodeToString(m.Sum(nil)[:12])
}

// list reads the state into rows, each carrying its key's rowID, and the
// state's place in the file.
func (in *Instance) list() view {
	st := in.store.State()
	held := in.opts.Held()
	v := view{place: st.Place()}
	v.At = fmt.Sprintf("%d.%s.%t", v.place.Line, v.place.Prefix, held)
	if st.Err != nil {
		v.Unreadable = "The safety state cannot be read, so every message and read receipt is refused. Nothing here can clear it until the state is readable again; running slack-mcp quarantine list in a terminal names the error."
		return v
	}
	v.Malformed = len(st.Malformed)
	if st.Strikes > 0 || st.LockEngaged() || held {
		title := fmt.Sprintf("Strikes: %d of %d", st.Strikes, st.Limit)
		scope := "Another block engages the strike lock."
		switch {
		case st.LockEngaged():
			title = fmt.Sprintf("Strike lock engaged (%d of %d)", st.Strikes, st.Limit)
			scope = "Every message and read receipt is refused, to anyone."
		case held:
			title = "Writes held: a block could not be recorded"
			scope = "Every message and read receipt is refused, to anyone. Clearing the strikes releases the hold unless the safety state was replaced or unreadable since; then approve the pending lift request with slack-mcp approve, or restart the server."
		}
		lr := row{Key: safety.StrikesKey, ID: in.rowID(safety.StrikesKey), Title: title, Scope: scope}
		for _, rs := range st.StrikeReasons {
			lr.Reasons = append(lr.Reasons, in.opts.Describe(rs))
		}
		v.Lock = &lr
	}
	for _, k := range st.People {
		v.Rows = append(v.Rows, in.keyRow(st, k, "Every DM and group DM with them is closed to messages and read receipts."))
	}
	for _, k := range st.Conversations {
		v.Rows = append(v.Rows, in.keyRow(st, k, "Closed to messages and read receipts."))
	}
	disambiguate(st, v.Rows)
	return v
}

func (in *Instance) keyRow(st safety.QuarantineState, k safety.Key, scope string) row {
	rw := row{Key: k, ID: in.rowID(k), Title: in.opts.Name(k), Scope: scope}
	if rs, ok := st.ReasonFor(k); ok {
		rw.Reasons = []string{in.opts.Describe(rs)}
	}
	return rw
}

// disambiguate tells apart rows whose names came out the same, as two
// conversations the cache cannot name do: by block time and position.
func disambiguate(st safety.QuarantineState, rows []row) {
	count := map[string]int{}
	for _, r := range rows {
		count[r.Title]++
	}
	seen := map[string]int{}
	for i, r := range rows {
		n := count[r.Title]
		if n < 2 {
			continue
		}
		seen[r.Title]++
		suffix := fmt.Sprintf(" (%d of %d)", seen[r.Title], n)
		if rs, ok := st.ReasonFor(r.Key); ok && !rs.Time.IsZero() {
			suffix = fmt.Sprintf(" (blocked %s; %d of %d)", rs.Time.Local().Format("Jan 2 15:04:05"), seen[r.Title], n)
		}
		rows[i].Title += suffix
	}
}

// answer applies the form to the rows of cur: Clear clears the checked
// rows, Done nothing. moved: the file changed since cur was read, and
// nothing was applied.
func (in *Instance) answer(form map[string][]string, cur view) (result string, moved bool) {
	if first(form["action"]) != "clear" {
		log.Printf("outbound-safety: clearing page closed without clearing")
		return "Closed without clearing anything. You can close this tab.", false
	}
	byID := map[string]safety.Key{}
	if cur.Lock != nil {
		byID[cur.Lock.ID] = cur.Lock.Key
	}
	for _, r := range cur.Rows {
		byID[r.ID] = r.Key
	}
	var keys []safety.Key
	seen := map[string]bool{}
	for _, id := range form["row"] {
		k, ok := byID[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return "Nothing was checked, so nothing was cleared. This page is closed; you can close this tab.", false
	}
	moved, ok, err := in.store.ClearKeysAt(keys, safety.ByWeb, cur.place, in.opts.Now())
	if moved {
		return "", true
	}
	// ok covers the keys applied, in order: all of them, or those before
	// the one that failed.
	cleared := 0
	for i := range ok {
		if k := keys[i]; ok[i] || k.Kind == safety.KeyStrikes {
			cleared++
			log.Printf("outbound-safety: CLEARED %s by=web", logKey(k))
		}
	}
	closed := " This page is closed; you can close this tab."
	if err != nil {
		log.Printf("outbound-safety: CLEAR by=web failed after %d of %d: %v", len(ok), len(keys), err)
		why := "running slack-mcp quarantine list in a terminal shows why."
		if len(ok) == 0 {
			return "Nothing could be cleared; " + why + closed, false
		}
		return fmt.Sprintf("Cleared %d; the rest could not be cleared; %s", cleared, why) + closed, false
	}
	msg := fmt.Sprintf("Cleared %d.", cleared)
	if cleared == 0 {
		msg = "Nothing needed clearing: what you checked was already clear."
	}
	return msg + closed, false
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func (in *Instance) render(w http.ResponseWriter, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.Execute(w, v); err != nil {
		log.Printf("unlock: render: %v", err)
	}
}

// logKey names a cleared key for the log: its kind, then its name and ID
// when it has them. The strikes key has neither.
func logKey(k safety.Key) string {
	parts := []string{string(k.Kind)}
	if k.Name != "" {
		parts = append(parts, k.Name)
	}
	if k.ID != "" {
		parts = append(parts, "("+k.ID+")")
	}
	return strings.Join(parts, " ")
}
