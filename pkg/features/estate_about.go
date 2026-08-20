package features

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// The about view: ADR-008's flagship. Breadth on the server — one-hop ego
// expansion from the folds, frontier communities, a ranked reading plan —
// depth in the agent (the reads), judgment with the human (precedence is
// reported as data, causation never asserted). The second hop runs only on
// an explicit deeper=true, and the one-hop response ends with the exact
// deeper invocation to copy.

type readingStep struct {
	Why  string
	Next string
}

type aboutViewData struct {
	Person   *personViewData
	Families *estateFamiliesData
	Plan     []readingStep
	Deeper   bool
	// Convergence carries the second hop: the seed plus top counterparts.
	Convergence *convergenceViewData
}

func aboutView(ctx context.Context, ap *provider.ApiProvider, person string, days int, deeper bool) *aboutViewData {
	data := &aboutViewData{Deeper: deeper}
	data.Person = personView(ctx, ap, person, days)
	if data.Person.Miss != nil {
		return data
	}

	// The founder plane: families anchored on the seed, via the same path
	// the families view takes.
	data.Families = familiesData(ap, "", person, 5, 0)

	// The reading plan: top-ranked handles with the evidence that ranked
	// them and the question each read answers.
	for i, row := range data.Person.Footprint {
		if i >= 3 {
			break
		}
		label := labelFor(data.Person.Labels, row.Conv)
		next := fmt.Sprintf("messages target='%s'", label)
		if info, ok := data.Person.Labels[row.Conv]; ok && info.IsIM {
			next = fmt.Sprintf("messages target='%s'", strings.TrimPrefix(info.Label, "DM "))
		}
		data.Plan = append(data.Plan, readingStep{
			Why:  fmt.Sprintf("%s — their densest observed surface (%d active days) — read for current state", label, row.Days),
			Next: next,
		})
	}
	if len(data.Person.Counterparts) > 0 {
		top := data.Person.Counterparts[0]
		data.Plan = append(data.Plan, readingStep{
			Why: fmt.Sprintf("%s — top counterpart as observed (%d co-active days) — read for the working relationship",
				data.Person.Names[top.ID], top.CoDays+top.DMDays),
			Next: fmt.Sprintf("estate view='person' person='%s'", top.ID),
		})
	}

	// The second hop: compile the top counterparts' activity and show where
	// the seed's circle converges. Bounded: three people, two pages each.
	if deeper && len(data.Person.Counterparts) > 0 {
		people := []string{person}
		for i, row := range data.Person.Counterparts {
			if i >= 3 {
				break
			}
			people = append(people, row.ID)
		}
		data.Convergence = convergenceView(ctx, ap, people, days)
	}
	return data
}

// ---- renderers ----

// pageWindow slices a ranked list at [offset, offset+cap) and reports how
// many items remain beyond the window — a cap never prevents paging.
func pageWindow(total, offset, cap_ int) (from, to, remaining int) {
	if offset >= total {
		return 0, 0, 0
	}
	to = offset + cap_
	if to > total {
		to = total
	}
	return offset, to, total - to
}

func renderPlaneFooter(b *strings.Builder, cov provider.EstateCoverageInfo, att provider.AttentionInfo, compiled *provider.CompiledStats) {
	b.WriteString("---\n")
	if compiled != nil {
		fmt.Fprintf(b, "Coverage: compiled executor — %d search calls in %s, %d matches recorded as encounters; joins ran in the fold.",
			compiled.Calls, compiled.Elapsed.Round(10_000_000), compiled.Matches)
		for handle, n := range compiled.Truncated {
			fmt.Fprintf(b, " %s truncated (%d matches beyond the page cap).", handle, n)
		}
		for handle, msg := range compiled.Failed {
			fmt.Fprintf(b, " %s search failed: %s.", handle, msg)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("Coverage: fold executor, 0 Slack calls.\n")
	}
	if att.Available && att.Stats.Events > 0 {
		fmt.Fprintf(b, "Activity plane: %d encounters, %d people, %d conversations (%s → %s), as observed by this agent's reading; the ninety-day window bounds it.\n",
			att.Stats.Events, att.Stats.Users, att.Stats.Convs, att.Stats.FirstDay, att.Stats.LastDay)
	} else if att.Available {
		b.WriteString("Activity plane: observing — nothing recorded yet; rankings will sharpen as reads accumulate.\n")
	} else {
		b.WriteString("Activity plane: no attention ledger open — activity claims unavailable.\n")
	}
	if !cov.LastFullSweep.IsZero() {
		fmt.Fprintf(b, "Estate: %d channels, last full sweep %s.\n", cov.Channels, cov.LastFullSweep.Format("2006-01-02 15:04"))
	}
}

func renderMiss(b *strings.Builder, res *provider.PersonResolution) {
	fmt.Fprintf(b, "Could not resolve %q (%s).\n", res.Input, res.Reason)
	if len(res.Candidates) > 0 {
		b.WriteString("\nClosest matches:\n")
		for _, c := range res.Candidates {
			line := fmt.Sprintf("- @%s — %s", c.Handle, c.DisplayName)
			if c.Title != "" {
				line += " (" + c.Title + ")"
			}
			if c.GoneReason != "" {
				line += fmt.Sprintf(" [gone: %s %s]", c.GoneReason, strings.Join(c.GoneBetween, "…"))
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n**Next:** re-query with a handle from the list.")
	} else {
		b.WriteString("**Next:** search the directory: estate view='people' person='<name>'")
	}
}

func formatPersonView(v *personViewData) string {
	var b strings.Builder
	if v.Miss != nil {
		b.WriteString("## Estate — person view\n\n")
		renderMiss(&b, v.Miss)
		return b.String()
	}
	fmt.Fprintf(&b, "## Estate — person view — %s", v.Label)
	if v.Title != "" {
		fmt.Fprintf(&b, " (%s)", v.Title)
	}
	fmt.Fprintf(&b, ", last %d days\n\n", v.Days)

	if len(v.Created) > 0 {
		fmt.Fprintf(&b, "Founder of %d channels: #%s\n\n", len(v.Created), strings.Join(v.Created, ", #"))
	}

	from, to, remaining := pageWindow(len(v.Footprint), v.Offset, 10)
	if len(v.Footprint) == 0 {
		b.WriteString("No activity observed in the window.\n")
	} else if from == to {
		fmt.Fprintf(&b, "Offset %d is past the end (%d surfaces).\n", v.Offset, len(v.Footprint))
	} else {
		fmt.Fprintf(&b, "Active surfaces %d–%d of %d, as observed:\n", from+1, to, len(v.Footprint))
		for _, row := range v.Footprint[from:to] {
			line := fmt.Sprintf("- %s — %d active days", labelFor(v.Labels, row.Conv), row.Days)
			if len(row.Strips) > 0 {
				last := row.Strips[len(row.Strips)-1]
				line += fmt.Sprintf(" (latest strip %s → %s)", last.From, last.To)
			}
			b.WriteString(line + "\n")
		}
		if remaining > 0 {
			fmt.Fprintf(&b, "… %d more — next page: estate view='person' person='%s' offset=%d\n", remaining, v.Handle, to)
		}
		b.WriteString("\n")
	}

	if len(v.Counterparts) > 0 {
		cfrom, cto, cleft := pageWindow(len(v.Counterparts), v.Offset, 5)
		if cfrom < cto {
			fmt.Fprintf(&b, "Counterparts %d–%d of %d, as observed:\n", cfrom+1, cto, len(v.Counterparts))
			for _, row := range v.Counterparts[cfrom:cto] {
				line := fmt.Sprintf("- %s — %d co-active days", v.Names[row.ID], row.CoDays)
				if row.DMDays > 0 {
					line += fmt.Sprintf(" + %d DM days", row.DMDays)
				}
				b.WriteString(line + "\n")
			}
			if cleft > 0 {
				fmt.Fprintf(&b, "… %d more — next page: estate view='person' person='%s' offset=%d\n", cleft, v.Handle, cto)
			}
			b.WriteString("\n")
		}
	}

	if v.Parallelism != nil && len(v.Parallelism) > 0 {
		b.WriteString("Hour-cadence parallelism (your own hours):\n")
		ns := make([]int, 0, len(v.Parallelism))
		for n := range v.Parallelism {
			ns = append(ns, n)
		}
		sort.Ints(ns)
		for _, n := range ns {
			fmt.Fprintf(&b, "- %d conversation(s): %d hours\n", n, v.Parallelism[n])
		}
		fmt.Fprintf(&b, "- peak: %d concurrent\n\n", v.Peak)
	}

	renderPlaneFooter(&b, v.Coverage, v.Attention, v.Compiled)
	fmt.Fprintf(&b, "This view rests on %d observed encounters in the window — thin numbers mean barely observed, never quiet.\n", v.WindowEncounters)
	fmt.Fprintf(&b, "**Next:** whole picture: estate view='about' person='%s' | shared windows: estate view='convergence' people='%s,<other>'", v.Handle, v.Handle)
	return b.String()
}

func formatInitiativesView(v *initiativesViewData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Estate — initiatives view — last %d days\n\n", v.Days)
	if len(v.Rows) == 0 {
		fmt.Fprintf(&b, "Nothing observed moving in the last %d days. Movement appears here as channels are read — or compile one person's activity: estate view='person' person='@<handle>'.\n\n", v.Days)
	}
	rfrom, rto, rleft := pageWindow(len(v.Rows), v.Offset, 10)
	for _, row := range v.Rows[rfrom:rto] {
		fmt.Fprintf(&b, "### %s — %d channel(s) moving\n", v.Names[row.Creator], len(row.Channels))
		if !row.CreatorActive {
			b.WriteString("(creator not observed in these channels this window)\n")
		}
		for _, ch := range row.Channels {
			fmt.Fprintf(&b, "- %s — %d active days, %d people observed\n", labelFor(v.Labels, ch.Conv), ch.ActiveDays, ch.People)
		}
		b.WriteString("\n")
	}
	if rleft > 0 {
		fmt.Fprintf(&b, "… %d more creators with observed movement — next page: estate view='initiatives' days=%d offset=%d\n\n", rleft, v.Days, rto)
	}
	renderPlaneFooter(&b, v.Coverage, v.Attention, nil)
	b.WriteString("**Next:** read a moving channel: messages target='#<name>' | the people: estate view='person' person='@<handle>'")
	return b.String()
}

func formatConvergenceView(v *convergenceViewData) string {
	var b strings.Builder
	labels := make([]string, 0, len(v.People))
	for _, l := range v.People {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	fmt.Fprintf(&b, "## Estate — convergence view — %s, last %d days\n\n", strings.Join(labels, " + "), v.Days)

	for i := range v.Misses {
		renderMiss(&b, &v.Misses[i])
		b.WriteString("\n")
	}
	if len(v.People) < 2 {
		b.WriteString("Convergence needs at least two resolved people.\n")
		return b.String()
	}

	if len(v.Cells) == 0 {
		b.WriteString("No co-active windows observed for this group.\n\n")
	} else {
		cfrom, cto, cleft := pageWindow(len(v.Cells), v.Offset, 10)
		fmt.Fprintf(&b, "Densest co-occurrence windows %d–%d of %d, as observed:\n", cfrom+1, cto, len(v.Cells))
		for _, cell := range v.Cells[cfrom:cto] {
			seen := make([]string, 0, len(cell.Seen))
			for _, id := range cell.Seen {
				seen = append(seen, v.People[id])
			}
			fmt.Fprintf(&b, "- %s — co-active %d day(s), %s → %s (%s)\n",
				labelFor(v.Labels, cell.Conv), cell.CoDays, cell.First, cell.Last, strings.Join(seen, ", "))
		}
		if cleft > 0 {
			fmt.Fprintf(&b, "… %d more — next page: same call with offset=%d\n", cleft, cto)
		}
		b.WriteString("\n")
	}

	if len(v.Baseline) > 0 {
		b.WriteString("Baselines (each person's active days in the window, all conversations):")
		ids := make([]string, 0, len(v.Baseline))
		for id := range v.Baseline {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(&b, " %s %d;", v.People[id], v.Baseline[id])
		}
		b.WriteString("\n\n")
	}

	renderPlaneFooter(&b, v.Coverage, v.Attention, v.Compiled)
	b.WriteString("**Next:** read a shared window: messages target='#<name>' since='" + fmt.Sprintf("%dd", v.Days) + "'")
	return b.String()
}

func formatAboutView(v *aboutViewData) string {
	var b strings.Builder
	if v.Person.Miss != nil {
		b.WriteString("## Estate — about\n\n")
		renderMiss(&b, v.Person.Miss)
		return b.String()
	}
	fmt.Fprintf(&b, "## Estate — about %s", v.Person.Label)
	if v.Person.Title != "" {
		fmt.Fprintf(&b, " (%s)", v.Person.Title)
	}
	b.WriteString("\n\n")

	if v.Families != nil && len(v.Families.Families) > 0 {
		b.WriteString("Founder plane (families containing their channels):\n")
		for i, f := range v.Families.Families {
			if i >= 4 {
				break
			}
			fmt.Fprintf(&b, "- %s — %d channels\n", f.Stem, len(f.Channels))
		}
		b.WriteString("\n")
	}

	if len(v.Person.Footprint) > 0 {
		b.WriteString("Activity plane (as observed):\n")
		for i, row := range v.Person.Footprint {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&b, "- %s — %d active days\n", labelFor(v.Person.Labels, row.Conv), row.Days)
		}
		b.WriteString("\n")
	}
	if len(v.Person.Counterparts) > 0 {
		b.WriteString("Circle (top counterparts, as observed):\n")
		for i, row := range v.Person.Counterparts {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&b, "- %s — %d co-active days\n", v.Person.Names[row.ID], row.CoDays+row.DMDays)
		}
		b.WriteString("\n")
	}

	b.WriteString("(Summary view — full lists page through view='person', view='families', view='convergence'.)\n\n")
	if len(v.Plan) > 0 {
		b.WriteString("Reading plan (ranked; depth is yours):\n")
		for i, step := range v.Plan {
			fmt.Fprintf(&b, "%d. %s\n   → %s\n", i+1, step.Why, step.Next)
		}
		b.WriteString("\n")
	}

	if v.Convergence != nil {
		for i := range v.Convergence.Misses {
			renderMiss(&b, &v.Convergence.Misses[i])
			b.WriteString("\n")
		}
		b.WriteString("Second hop — where the circle converges:\n")
		for i, cell := range v.Convergence.Cells {
			if i >= 5 {
				break
			}
			seen := make([]string, 0, len(cell.Seen))
			for _, id := range cell.Seen {
				seen = append(seen, v.Convergence.People[id])
			}
			fmt.Fprintf(&b, "- %s — co-active %d day(s) (%s)\n",
				labelFor(v.Convergence.Labels, cell.Conv), cell.CoDays, strings.Join(seen, ", "))
		}
		b.WriteString("\n")
	}

	compiled := v.Person.Compiled
	if v.Convergence != nil && v.Convergence.Compiled != nil {
		if compiled == nil {
			compiled = v.Convergence.Compiled
		} else {
			merged := *compiled
			merged.Calls += v.Convergence.Compiled.Calls
			merged.Elapsed += v.Convergence.Compiled.Elapsed
			merged.Matches += v.Convergence.Compiled.Matches
			merged.Truncated = map[string]int{}
			merged.Failed = map[string]string{}
			for _, src := range []*provider.CompiledStats{compiled, v.Convergence.Compiled} {
				for k, n := range src.Truncated {
					merged.Truncated[k] = n
				}
				for k, m := range src.Failed {
					merged.Failed[k] = m
				}
			}
			compiled = &merged
		}
	}
	renderPlaneFooter(&b, v.Person.Coverage, v.Person.Attention, compiled)
	if !v.Deeper {
		fmt.Fprintf(&b, "**Next:** scan deeper (compiles the circle's activity, bounded searches): estate view='about' person='%s' deeper=true", v.Person.Handle)
	} else {
		fmt.Fprintf(&b, "**Next:** follow the reading plan above; precedence is data, causation is yours to judge.")
	}
	return b.String()
}
