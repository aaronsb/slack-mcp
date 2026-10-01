package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/safety"
)

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
	fmt.Fprintf(c.stdout, "Destination: %s (%s, %s)\n", display(t.Key), termSafe(t.Key.ID), t.DestKind)
	if t.External {
		fmt.Fprintf(c.stdout, "External parties: %s\n", orNone(termSafeAll(t.Parties)))
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
		Source: safety.SourceCLI, Expires: exp, Parties: parties,
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
	fmt.Fprintf(c.stdout, "Posture: %s (%s); entries are labeled by it. Data %s\n", c.posture, src, c.ws.Dir)
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
				how += ", " + termSafe(e.PendingID)
			}
			how += ")"
		}
		exp := "never"
		if e.Expires != nil {
			exp = e.Expires.Format(time.RFC3339)
		}
		fmt.Fprintf(c.stdout, "  %s  %s  %s  cases=%s  added=%s by %s  expires=%s  [%s]\n",
			c.label(e.Key, "add time"), termSafe(e.Key.ID), kindLabel(e), safety.CasesString(e.Cases),
			e.Added.Format(time.RFC3339), how, exp, l.State)
		if len(e.Parties) > 0 {
			fmt.Fprintf(c.stdout, "      external parties recorded: %s\n", strings.Join(termSafeAll(e.Parties), ", "))
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
