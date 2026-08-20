package estate

import (
	"sort"
	"time"
)

// A Strip is a maximal run of active day-buckets: the temporal shape of the
// activity plane (ADR-008). Gap tolerance is in days — a one-day gap inside
// otherwise continuous activity does not split the run.
type Strip struct {
	From string // first active day, "2006-01-02"
	To   string // last active day
	Days int    // distinct active days inside the strip
}

// Strips folds encounters into maximal runs. Hour resolution collapses to
// days here; the hour plane is queried separately. No windowing happens
// here — callers window first.
func Strips(encs []Encounter, gapDays int) []Strip {
	if len(encs) == 0 {
		return nil
	}
	daySet := map[string]struct{}{}
	for _, e := range encs {
		if e.Day != "" {
			daySet[e.Day] = struct{}{}
		}
	}
	days := make([]string, 0, len(daySet))
	for d := range daySet {
		days = append(days, d)
	}
	sort.Strings(days)

	var out []Strip
	cur := Strip{From: days[0], To: days[0], Days: 1}
	prev, _ := time.Parse("2006-01-02", days[0])
	for _, d := range days[1:] {
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			continue
		}
		if int(t.Sub(prev).Hours()/24) > gapDays+1 {
			out = append(out, cur)
			cur = Strip{From: d, To: d, Days: 1}
		} else {
			cur.To = d
			cur.Days++
		}
		prev = t
	}
	return append(out, cur)
}

// ActiveDays returns the distinct days with encounters at or after the
// since day ("" = no bound), sorted ascending.
func ActiveDays(encs []Encounter, since string) []string {
	daySet := map[string]struct{}{}
	for _, e := range encs {
		if e.Day != "" && e.Day >= since {
			daySet[e.Day] = struct{}{}
		}
	}
	days := make([]string, 0, len(daySet))
	for d := range daySet {
		days = append(days, d)
	}
	sort.Strings(days)
	return days
}
