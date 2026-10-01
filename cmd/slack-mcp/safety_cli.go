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
	"unicode/utf8"

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
	open       func(org safety.Org, p safety.Posture) (*safety.Workspace, error)

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
	if c.ws, err = c.open(c.dir.Org(), c.posture); err != nil {
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
		c.expirePending()
		if len(rest) == 0 {
			return c.pendingList()
		}
		return c.answer(rest[0], true)
	default: // deny
		c.expirePending()
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
		return termSafe(n)
	}
	if k.Name != "" {
		return termSafe(k.Name) + " (name at " + when + ")"
	}
	return termSafe(k.ID)
}

// header names the workspace, the posture, and the data directory the
// state was read from.
func (c *safetyCLI) header() {
	fmt.Fprintf(c.stdout, "Workspace %s, posture %s, data %s\n", c.ws.TeamID, c.posture, c.ws.Dir)
}

// display is the name an operator types back: the recorded name, else the
// ID.
func display(k safety.Key) string {
	if k.Kind == safety.KeyStrikes {
		return "strikes"
	}
	if k.Name != "" {
		return termSafe(k.Name)
	}
	return termSafe(k.ID)
}

// matchKey finds the key arg names among ks: by ID, or by recorded name.
// A sigil narrows the kind: #name matches conversations only, @name people
// only; a bare word matches either.
func matchKey(arg string, ks []safety.Key) (safety.Key, bool) {
	want := safety.KeyKind("")
	bare := arg
	switch {
	case strings.HasPrefix(arg, "#"):
		want, bare = safety.KeyConversation, arg[1:]
	case strings.HasPrefix(arg, "@"):
		want, bare = safety.KeyPerson, arg[1:]
	}
	for _, k := range ks {
		if want != "" && k.Kind != want {
			continue
		}
		if k.ID == bare || (bare != "" && strings.TrimLeft(k.Name, "@#") == bare) {
			return k, true
		}
	}
	return safety.Key{}, false
}

// --- helpers ---

// termSafe escapes control characters before text reaches the operator's
// terminal: C0 controls other than newline and tab, DEL, C1 controls, and
// bytes that are not UTF-8, so text an agent supplied cannot move the
// cursor, retitle the window, or write the clipboard (OSC 52); and
// bidirectional and zero-width formatting characters, so it cannot reorder
// or hide what the operator reads before approving.
func termSafe(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			fmt.Fprintf(&b, "\\x%02x", s[i])
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\x%02x", r)
		case r >= 0x80 && r <= 0x9f, invisibleFormat(r):
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// invisibleFormat reports the bidi controls (U+061C, U+200E-200F,
// U+202A-202E, U+2066-2069) and zero-width characters (U+200B-200D,
// U+FEFF) termSafe escapes.
func invisibleFormat(r rune) bool {
	return r == 0x061c || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) ||
		(r >= 0x2066 && r <= 0x2069) || r == 0xfeff
}

// termSafeAll escapes each string.
func termSafeAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = termSafe(s)
	}
	return out
}

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
