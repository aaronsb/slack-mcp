package main

import (
	"fmt"

	"github.com/aaronsb/slack-mcp/pkg/safety"
)

func (c *safetyCLI) quarantineList() int {
	st := c.ws.Quarantine.State()
	c.header()
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
		fmt.Fprintf(c.stdout, "  %s  %s  person (every DM and group DM with them)\n", c.label(k, "block time"), termSafe(k.ID))
	}
	for _, k := range st.Conversations {
		fmt.Fprintf(c.stdout, "  %s  %s  conversation\n", c.label(k, "block time"), termSafe(k.ID))
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
		fmt.Fprintf(c.stdout, "Clearing the quarantine on %s (%s, %s).\n", c.label(k, "block time"), termSafe(k.ID), k.Kind)
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
	if k.Kind != safety.KeyStrikes && c.ws.Trust.State().InEffect(k, c.now()) {
		fmt.Fprintf(c.stdout, "%s is a trusted destination; its trust applies again now.\n", display(k))
	}
	return 0
}
