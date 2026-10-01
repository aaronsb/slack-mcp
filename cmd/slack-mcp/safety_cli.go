package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/aaronsb/slack-mcp/pkg/safety"
)

// The operator's safety controls (ADR-013): quarantine, trust, approve, and
// deny. They run in the operator's terminal, never as tools.
var safetyCommands = map[string]bool{"quarantine": true, "trust": true, "approve": true, "deny": true}

func isSafetyCommand(name string) bool { return safetyCommands[name] }

const safetyUsage = `usage:
  slack-mcp quarantine list
  slack-mcp quarantine clear <@handle|#channel|ID|strikes>
  slack-mcp trust add <@handle|#channel|ID> [--case external|cross-conversation|all] [--for 12h|30d]
  slack-mcp trust list
  slack-mcp trust remove <@handle|#channel|ID>
  slack-mcp approve [<id>]
  slack-mcp deny <id>`

// runSafetyCommand runs a safety subcommand from main and returns the exit
// code. The .env allowlist applies here as in the server (ADR-014).
func runSafetyCommand(args []string) int {
	if err := loadDotEnv(".env", os.LookupEnv, os.Setenv); err != nil {
		fmt.Fprintln(os.Stderr, "slack-mcp:", err)
		return 1
	}
	c := &safetyCLI{
		stdin:      os.Stdin,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		isTerminal: stdinIsTerminal,
		lookupEnv:  os.LookupEnv,
		now:        time.Now,
		connect:    connectSlackDirectory,
		open:       safety.Open,
	}
	return c.run(args)
}

func stdinIsTerminal() bool {
	fd := os.Stdin.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// directory is what the CLI needs from Slack: the account's organization
// (from auth.test), destinations resolved to IDs, and current names.
type directory interface {
	Org() safety.Org
	Resolve(arg string) (cliTarget, error)
	CurrentName(k safety.Key) (string, bool)
}

// cliTarget is a destination the operator named.
type cliTarget struct {
	Key      safety.Key
	DestKind safety.DestKind
	External bool
	// Parties are the external parties the gate would find now: team IDs
	// for a channel, user IDs for a DM or group DM.
	Parties []string
}

type safetyCLI struct {
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	isTerminal func() bool
	lookupEnv  func(string) (string, bool)
	now        func() time.Time
	connect    func() (directory, error)
	open       func(teamID string, p safety.Posture) (*safety.Workspace, error)

	posture safety.Posture
	dir     directory
	ws      *safety.Workspace
	in      *bufio.Reader
}

func (c *safetyCLI) fail(format string, a ...any) int {
	fmt.Fprintf(c.stderr, "slack-mcp: "+format+"\n", a...)
	return 1
}

func (c *safetyCLI) usage() int {
	fmt.Fprintln(c.stderr, safetyUsage)
	return 2
}

func (c *safetyCLI) run(args []string) int {
	if len(args) == 0 {
		return c.usage()
	}
	raw, _ := c.lookupEnv(safety.SafetyEnv)
	p, err := safety.ParsePosture(raw)
	if err != nil {
		return c.fail("%v", err)
	}
	c.posture = p

	cmd, rest := args[0], args[1:]
	sub := ""
	if cmd == "quarantine" || cmd == "trust" {
		if len(rest) == 0 {
			return c.usage()
		}
		sub, rest = rest[0], rest[1:]
	}

	// Commands that change state ask for a terminal and a typed
	// confirmation, checked before anything else happens. trust remove
	// only reduces privilege and needs neither.
	needsTTY := false
	switch {
	case cmd == "approve" && len(rest) > 0, cmd == "deny",
		cmd == "quarantine" && sub == "clear", cmd == "trust" && sub == "add":
		needsTTY = true
	}
	if needsTTY && !c.isTerminal() {
		return c.fail("%s needs an interactive terminal on stdin; nothing changed", strings.TrimSpace(cmd+" "+sub))
	}

	// Validate arguments before any network call.
	var tadd trustAddArgs
	switch cmd + " " + sub {
	case "quarantine list", "trust list":
		if len(rest) != 0 {
			return c.usage()
		}
	case "quarantine clear", "trust remove":
		if len(rest) != 1 {
			return c.usage()
		}
	case "trust add":
		if tadd, err = parseTrustAdd(rest); err != nil {
			fmt.Fprintln(c.stderr, "slack-mcp:", err)
			return c.usage()
		}
	case "approve ":
		if len(rest) > 1 {
			return c.usage()
		}
	case "deny ":
		if len(rest) != 1 {
			return c.usage()
		}
	default:
		return c.usage()
	}

	if c.dir, err = c.connect(); err != nil {
		return c.fail("%v", err)
	}
	if c.ws, err = c.open(c.dir.Org().TeamID, c.posture); err != nil {
		return c.fail("%v", err)
	}
	c.in = bufio.NewReader(c.stdin)

	switch cmd + " " + sub {
	case "quarantine list":
		return c.quarantineList()
	case "quarantine clear":
		return c.quarantineClear(rest[0])
	case "trust add":
		return c.trustAdd(tadd)
	case "trust list":
		return c.trustList()
	case "trust remove":
		return c.trustRemove(rest[0])
	case "approve ":
		if len(rest) == 0 {
			return c.pendingList()
		}
		return c.answer(rest[0], true)
	default: // deny
		return c.answer(rest[0], false)
	}
}

// confirm asks the operator to type want back. Anything else changes
// nothing.
func (c *safetyCLI) confirm(prompt, want string) bool {
	fmt.Fprintf(c.stdout, "%s ", strings.TrimSpace(prompt+" Type "+want+" to confirm:"))
	line, _ := c.in.ReadString('\n')
	if strings.TrimSpace(line) != want {
		fmt.Fprintln(c.stderr, "slack-mcp: confirmation did not match; nothing changed")
		return false
	}
	return true
}

// label is how a key is shown: the current name, else the recorded one
// marked as such, else the ID.
func (c *safetyCLI) label(k safety.Key, when string) string {
	if n, ok := c.dir.CurrentName(k); ok && n != "" {
		return n
	}
	if k.Name != "" {
		return k.Name + " (name at " + when + ")"
	}
	return k.ID
}

// display is the name an operator types back: the recorded name, else the
// ID.
func display(k safety.Key) string {
	if k.Kind == safety.KeyStrikes {
		return "strikes"
	}
	if k.Name != "" {
		return k.Name
	}
	return k.ID
}

// matchKey finds the key arg names among ks: by ID, or by recorded name
// with or without its @ or #.
func matchKey(arg string, ks []safety.Key) (safety.Key, bool) {
	bare := strings.TrimLeft(arg, "@#")
	for _, k := range ks {
		if k.ID == arg || (bare != "" && strings.TrimLeft(k.Name, "@#") == bare) {
			return k, true
		}
	}
	return safety.Key{}, false
}

// --- quarantine ---

func (c *safetyCLI) quarantineList() int {
	st := c.ws.Quarantine.State()
	fmt.Fprintf(c.stdout, "Workspace %s, posture %s.\n", c.ws.TeamID, c.posture)
	if st.Err != nil {
		fmt.Fprintf(c.stdout, "Quarantine state could not be read (%v): every say and mark-read is refused.\n", rootErr(st.Err))
		return 0
	}
	switch {
	case st.LockEngaged():
		fmt.Fprintf(c.stdout, "Strike lock engaged (%d of %d): every say and mark-read is refused. Clear with: quarantine clear strikes\n", st.Strikes, st.Limit)
	case st.Strikes > 0:
		fmt.Fprintf(c.stdout, "Strikes: %d of %d.\n", st.Strikes, st.Limit)
	}
	if len(st.People) == 0 && len(st.Conversations) == 0 {
		fmt.Fprintln(c.stdout, "Nothing quarantined.")
	}
	for _, k := range st.People {
		fmt.Fprintf(c.stdout, "  %s  %s  person (every DM and group DM with them)\n", c.label(k, "block time"), k.ID)
	}
	for _, k := range st.Conversations {
		fmt.Fprintf(c.stdout, "  %s  %s  conversation\n", c.label(k, "block time"), k.ID)
	}
	if len(st.Malformed) > 0 {
		fmt.Fprintf(c.stdout, "Skipped %d unreadable line(s): %s\n", len(st.Malformed), joinInts(st.Malformed))
	}
	return 0
}

func (c *safetyCLI) quarantineClear(arg string) int {
	st := c.ws.Quarantine.State()
	if st.Err != nil {
		return c.fail("quarantine state could not be read (%v); fix that first", rootErr(st.Err))
	}
	var k safety.Key
	if arg == "strikes" {
		if !st.LockEngaged() && st.Strikes == 0 {
			fmt.Fprintln(c.stdout, "No strikes to clear.")
			return 0
		}
		k = safety.StrikesKey
		fmt.Fprintf(c.stdout, "Clearing %d strike(s)", st.Strikes)
		if st.LockEngaged() {
			fmt.Fprint(c.stdout, " and the strike lock")
		}
		fmt.Fprintln(c.stdout, ".")
	} else {
		found, ok := matchKey(arg, append(append([]safety.Key{}, st.People...), st.Conversations...))
		if !ok {
			t, err := c.dir.Resolve(arg)
			if err != nil {
				return c.fail("%v", err)
			}
			if found, ok = st.IsQuarantined(t.Key.ID); !ok {
				return c.fail("%s is not quarantined; nothing changed", arg)
			}
		}
		k = found
		fmt.Fprintf(c.stdout, "Clearing the quarantine on %s (%s, %s).\n", c.label(k, "block time"), k.ID, k.Kind)
	}
	if !c.confirm("", display(k)) {
		return 1
	}
	ok, err := c.ws.Quarantine.Clear(k, safety.ByCLI, "", c.now())
	if err != nil {
		return c.fail("%v", err)
	}
	if !ok {
		fmt.Fprintln(c.stdout, "Already clear.")
		return 0
	}
	fmt.Fprintln(c.stdout, "Cleared.")
	if k.Kind != safety.KeyStrikes && c.ws.Trust.State().Entries(k) {
		fmt.Fprintf(c.stdout, "%s is a trusted destination; its trust applies again now.\n", display(k))
	}
	return 0
}

// --- trust ---

type trustAddArgs struct {
	dest  string
	cases []safety.Case
	dur   time.Duration
}

func parseTrustAdd(args []string) (trustAddArgs, error) {
	a := trustAddArgs{cases: []safety.Case{safety.CaseExternal, safety.CaseCrossConversation}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, val, hasVal := strings.Cut(arg, "=")
		if name == "--case" || name == "--for" {
			if !hasVal {
				if i+1 >= len(args) {
					return a, fmt.Errorf("%s needs a value", name)
				}
				i++
				val = args[i]
			}
			switch name {
			case "--case":
				switch val {
				case "all":
					a.cases = []safety.Case{safety.CaseExternal, safety.CaseCrossConversation}
				case string(safety.CaseExternal), string(safety.CaseCrossConversation):
					a.cases = []safety.Case{safety.Case(val)}
				default:
					return a, fmt.Errorf("--case %q: want external, cross-conversation, or all", val)
				}
			case "--for":
				d, err := parseDuration(val)
				if err != nil {
					return a, err
				}
				a.dur = d
			}
			continue
		}
		if strings.HasPrefix(arg, "-") || a.dest != "" {
			return a, fmt.Errorf("unexpected argument %q", arg)
		}
		a.dest = arg
	}
	if a.dest == "" {
		return a, errors.New("trust add needs a destination")
	}
	return a, nil
}

// parseDuration takes Go durations and whole days ("30d").
func parseDuration(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	} else if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, nil
	}
	return 0, fmt.Errorf("--for %q: want a duration like 12h or 30d", s)
}

func (c *safetyCLI) trustAdd(a trustAddArgs) int {
	t, err := c.dir.Resolve(a.dest)
	if err != nil {
		return c.fail("%v", err)
	}
	if t.DestKind == safety.DestSelf {
		return c.fail("the self-DM is never external and needs no trust")
	}
	fmt.Fprintf(c.stdout, "Destination: %s (%s, %s)\n", display(t.Key), t.Key.ID, t.DestKind)
	if t.External {
		fmt.Fprintf(c.stdout, "External parties: %s\n", orNone(t.Parties))
	} else {
		fmt.Fprintln(c.stdout, "External parties: none (internal now)")
	}
	fmt.Fprintf(c.stdout, "Cases: %s\n", safety.CasesString(a.cases))
	var exp *time.Time
	if a.dur > 0 {
		e := c.now().Add(a.dur).UTC()
		exp = &e
		fmt.Fprintf(c.stdout, "Expires: %s\n", e.Format(time.RFC3339))
	} else {
		fmt.Fprintln(c.stdout, "Expires: never")
	}
	if !c.confirm("", display(t.Key)) {
		return 1
	}
	var parties []string
	if t.Key.Kind == safety.KeyConversation {
		parties = t.Parties
	}
	err = c.ws.Trust.Add(safety.TrustAdd{
		Time: c.now(), Key: t.Key, DestKind: t.DestKind, Cases: a.cases,
		Source: safety.SourceCLI, Posture: c.posture, Expires: exp, Parties: parties,
	})
	if err != nil {
		return c.fail("%v", err)
	}
	fmt.Fprintf(c.stdout, "Trusted %s for %s. Requests already pending still need approve or deny.\n", display(t.Key), safety.CasesString(a.cases))
	return 0
}

func (c *safetyCLI) trustRemove(arg string) int {
	ts := c.ws.Trust.State()
	var keys []safety.Key
	for _, l := range ts.List(c.now(), safety.QuarantineState{}) {
		keys = append(keys, l.Entry.Key)
	}
	k, ok := matchKey(arg, keys)
	if !ok {
		t, err := c.dir.Resolve(arg)
		if err != nil {
			return c.fail("%v", err)
		}
		k = t.Key
	}
	removed, err := c.ws.Trust.Remove(k, c.now())
	if err != nil {
		return c.fail("%v", err)
	}
	if !removed {
		return c.fail("no trust recorded for %s", arg)
	}
	fmt.Fprintf(c.stdout, "Removed trust for %s.\n", display(k))
	return 0
}

func (c *safetyCLI) trustList() int {
	ts := c.ws.Trust.State()
	src := "default"
	if v, ok := c.lookupEnv(safety.SafetyEnv); ok && strings.TrimSpace(v) != "" {
		src = safety.SafetyEnv
	}
	fmt.Fprintf(c.stdout, "Posture: %s (%s); entries are labeled by it.\n", c.posture, src)
	if ts.Err != nil {
		fmt.Fprintf(c.stdout, "Trust list could not be read (%v): nothing is trusted.\n", rootErr(ts.Err))
		return 0
	}
	now := c.now()
	list := ts.List(now, c.ws.Quarantine.State())
	if len(list) == 0 {
		fmt.Fprintln(c.stdout, "No trusted destinations.")
	}
	for _, l := range list {
		e := l.Entry
		how := string(e.Source)
		if e.Source == safety.SourceElicitation {
			how += " (" + string(e.Posture)
			if e.PendingID != "" {
				how += ", " + e.PendingID
			}
			how += ")"
		}
		exp := "never"
		if e.Expires != nil {
			exp = e.Expires.Format(time.RFC3339)
		}
		fmt.Fprintf(c.stdout, "  %s  %s  %s  cases=%s  added=%s by %s  expires=%s  [%s]\n",
			c.label(e.Key, "add time"), e.Key.ID, kindLabel(e), safety.CasesString(e.Cases),
			e.Added.Format(time.RFC3339), how, exp, l.State)
		if len(e.Parties) > 0 {
			fmt.Fprintf(c.stdout, "      external parties recorded: %s\n", strings.Join(e.Parties, ", "))
		}
	}
	if len(ts.Malformed) > 0 {
		fmt.Fprintf(c.stdout, "Skipped %d unreadable line(s): %s\n", len(ts.Malformed), joinInts(ts.Malformed))
	}
	return 0
}

func kindLabel(e safety.TrustEntry) string {
	if e.DestKind != "" {
		return string(e.DestKind)
	}
	return string(e.Key.Kind)
}

// --- pending ---

func (c *safetyCLI) pendingList() int {
	now := c.now()
	if err := c.ws.Pending.Err(); err != nil {
		return c.fail("pending requests could not be read: %v", rootErr(err))
	}
	reqs := c.ws.Pending.List(now)
	if len(reqs) == 0 {
		fmt.Fprintln(c.stdout, "No pending requests.")
		return 0
	}
	for _, r := range reqs {
		state := ""
		if r.Status == safety.StatusApproved {
			state = "  approved, awaiting the call"
		}
		fmt.Fprintf(c.stdout, "  %s  %s  %s  age %s  expires in %s%s\n", r.ID, safety.CasesString(r.Cases),
			summary(r), now.Sub(r.Created).Round(time.Minute), r.Expires.Sub(now).Round(time.Minute), state)
	}
	return 0
}

func summary(r safety.Request) string {
	d := r.Destination
	s := fmt.Sprintf("%s to %s (%s)", r.Tool, d.Name, d.ID())
	if r.FileCount > 0 {
		s += fmt.Sprintf(" files=%d", r.FileCount)
	}
	if len(r.From) > 0 {
		s += " from=" + strings.Join(r.From, ",")
	}
	if r.IsLift() {
		var ks []string
		for _, k := range r.Lift {
			ks = append(ks, display(k))
		}
		s += " lift=" + strings.Join(ks, ",")
	}
	return s
}

// answer approves or denies a request after showing it in full: for case 1
// the text and file names, not a hash.
func (c *safetyCLI) answer(id string, approve bool) int {
	now := c.now()
	r, ok := c.ws.Pending.Lookup(id, now)
	if !ok {
		return c.fail("%s: %v", id, safety.ErrNoRequest)
	}
	if r.Status != safety.StatusPending && !(r.Status == safety.StatusApproved && !approve) {
		return c.fail("%s is %s; nothing changed", id, r.Status)
	}
	fmt.Fprintf(c.stdout, "Request %s: %s\n", r.ID, safety.CasesString(r.Cases))
	fmt.Fprintf(c.stdout, "  %s\n", summary(r))
	fmt.Fprintf(c.stdout, "  issued %s, expires %s\n", r.Created.Format(time.RFC3339), r.Expires.Format(time.RFC3339))
	if r.Text != "" {
		fmt.Fprintf(c.stdout, "  text:\n%s\n", indent(r.Text))
	}
	for i, n := range r.FileNames {
		fmt.Fprintf(c.stdout, "  file %d: %s\n", i+1, n)
	}
	verb := "deny"
	if approve {
		verb = "approve"
	}
	if !c.confirm("To "+verb+" it:", r.ID) {
		return 1
	}
	if !approve {
		if _, err := c.ws.Pending.Deny(id, safety.AnswerCLI, now); err != nil {
			return c.fail("%v", err)
		}
		fmt.Fprintf(c.stdout, "Denied %s.\n", id)
		return 0
	}
	if _, err := c.ws.Approve(id, now); err != nil {
		return c.fail("%v", err)
	}
	if r.IsLift() {
		fmt.Fprintf(c.stdout, "Approved %s: lifted. Nothing was sent; the agent calls again.\n", id)
	} else {
		fmt.Fprintf(c.stdout, "Approved %s: the next matching call goes through once.\n", id)
	}
	return 0
}

// --- helpers ---

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

func orNone(ss []string) string {
	if len(ss) == 0 {
		return "none found"
	}
	return strings.Join(ss, ", ")
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

// rootErr strips the path from a file error, so output names the error
// ("permission denied") and not the file.
func rootErr(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
