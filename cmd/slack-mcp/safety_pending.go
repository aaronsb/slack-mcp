package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/safety"
)

func (c *safetyCLI) pendingList() int {
	now := c.now()
	if err := c.ws.Pending.Err(); err != nil {
		return c.fail("pending requests could not be read: %v", rootErr(err))
	}
	c.header()
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
	s := fmt.Sprintf("%s to %s", termSafe(r.Tool), termSafe(d.Name))
	if id := d.ID(); id != "" {
		s += fmt.Sprintf(" (%s)", termSafe(id))
	}
	if r.FileCount > 0 {
		s += fmt.Sprintf(" files=%d", r.FileCount)
	}
	if len(r.From) > 0 {
		s += " from=" + strings.Join(termSafeAll(r.From), ",")
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
// the text and file names, not a hash. The answer carries the request shown,
// so it is refused if another request holds the ID by the time it lands.
func (c *safetyCLI) answer(id string, approve bool) int {
	now := c.now()
	r, ok := c.ws.Pending.Lookup(id, now)
	if !ok {
		return c.fail("%s: %v", id, safety.ErrNoRequest)
	}
	if r.Status != safety.StatusPending && !(r.Status == safety.StatusApproved && !approve) {
		return c.fail("%s is %s; nothing changed", id, r.Status)
	}
	fmt.Fprintf(c.stdout, "Request %s: %s\n", termSafe(r.ID), safety.CasesString(r.Cases))
	fmt.Fprintf(c.stdout, "  %s\n", summary(r))
	fmt.Fprintf(c.stdout, "  issued %s, expires %s\n", r.Created.Format(time.RFC3339), r.Expires.Format(time.RFC3339))
	if r.Text != "" {
		fmt.Fprintf(c.stdout, "  text:\n%s\n", indent(termSafe(r.Text)))
	}
	for i, n := range r.FileNames {
		fmt.Fprintf(c.stdout, "  file %d: %s\n", i+1, termSafe(n))
	}
	verb := "deny"
	if approve {
		verb = "approve"
	}
	if !c.confirm("To "+verb+" it:", r.ID) {
		return 1
	}
	if !approve {
		if _, err := c.ws.Pending.Deny(r, safety.AnswerCLI, now); err != nil {
			return c.fail("%v", err)
		}
		fmt.Fprintf(c.stdout, "Denied %s.\n", id)
		return 0
	}
	if _, err := c.ws.Approve(r, now); err != nil {
		return c.fail("%v", err)
	}
	if r.IsLift() {
		fmt.Fprintf(c.stdout, "Approved %s: lifted. Nothing was sent; the agent calls again.\n", id)
	} else {
		fmt.Fprintf(c.stdout, "Approved %s: the next matching call goes through once.\n", id)
	}
	return 0
}

// expirePending drops expired requests, and the content held for them,
// before any approve or deny. A failure is reported and does not stop the
// command: an expired request is refused either way.
func (c *safetyCLI) expirePending() {
	if _, err := c.ws.Pending.Expire(c.now()); err != nil {
		fmt.Fprintf(c.stderr, "slack-mcp: could not drop expired requests: %v\n", rootErr(err))
	}
}
