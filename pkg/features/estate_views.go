package features

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// EstateViews is ADR-008's read surface: named views that answer whole
// relationship questions from the fold, so the agent never does set math
// over listings. about is the entry point; families reads the founder
// plane; person, initiatives, and convergence read the activity plane the
// encounter observer accumulates.
var EstateViews = &Feature{
	Name:        "estate",
	Description: "Relationship views over the accumulated estate. view='about' answers \"tell me about X\" whole: founder plane, activity plane, circle, and a ranked reading plan. Other views: 'families' (engagement channels by stem), 'person' (footprint + counterparts), 'initiatives' (what moved, by creator), 'convergence' (where a group co-occurs). Fold-first at zero Slack calls; bounded compiled searches only when coverage requires.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"view": map[string]interface{}{
				"type":        "string",
				"description": "Which view: 'about', 'families', 'person', 'initiatives', 'convergence'",
			},
			"person": map[string]interface{}{
				"type":        "string",
				"description": "The seed person for about/person, or the creator anchor for families (@handle, name, or user ID)",
			},
			"people": map[string]interface{}{
				"type":        "string",
				"description": "Comma-separated people for the convergence view (at least two)",
			},
			"search": map[string]interface{}{
				"type":        "string",
				"description": "families only: filter by stem or channel name (partial match); relaxes the default criteria",
			},
			"days": map[string]interface{}{
				"type":        "number",
				"description": "Activity window in days (defaults: about/person 30, convergence 14, initiatives 7; capped at 90 — the attention ledger's horizon)",
			},
			"deeper": map[string]interface{}{
				"type":        "boolean",
				"description": "about only: second hop — compile the circle's activity with bounded searches and show where it converges",
				"default":     false,
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "families only: maximum families to return (default 10, max 50)",
				"default":     10,
			},
			"offset": map[string]interface{}{
				"type":        "number",
				"description": "Skip the first N items of the view's ranked lists — a cap never prevents paging; capped responses name the exact next call",
				"default":     0,
			},
		},
		"required": []string{"view"},
	},
	Handler: estateViewsHandler,
}

// famPhaseTokens marks the engagement-phase suffixes the families motif
// recognizes; ported from the ADR-008 evidence probe.
var famPhaseTokens = map[string]bool{
	"sales": true, "assessment": true, "implementation": true,
	"training": true, "support": true, "delivery": true,
	"migration": true, "onboarding": true, "discovery": true,
}

type famChannel struct {
	Name     string
	Creator  string
	Created  int64
	Archived bool
	// GoneFrom/GoneTo bound the observation interval of an absence
	// tombstone; zero for live channels.
	GoneFrom time.Time
	GoneTo   time.Time
}

type famGroup struct {
	Stem     string
	Channels []famChannel
	From, To int64
	Phased   int
}

type estateFamiliesData struct {
	Families      []famGroup
	TotalFamilies int
	Considered    int
	GoneIncluded  int
	FoldOwned     int
	Coverage      provider.EstateCoverageInfo
	Attention     provider.AttentionInfo
	CreatorNames  map[string]string
	Search        string
	PersonLabel   string
	PersonMiss    *provider.PersonResolution
	Shown         int
	Offset        int
	Limit         int
}

func viewOffset(params map[string]interface{}) int {
	if o, ok := params["offset"].(float64); ok && int(o) > 0 {
		return int(o)
	}
	return 0
}

func viewDays(params map[string]interface{}, def int) int {
	days := def
	if d, ok := params["days"].(float64); ok && int(d) > 0 {
		days = int(d)
	}
	if days > 90 {
		days = 90
	}
	return days
}

func estateViewsHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{Success: false, Message: "Internal error: provider not available"}, nil
	}

	view, _ := params["view"].(string)
	person, _ := params["person"].(string)
	person = strings.TrimSpace(person)
	deeper, _ := params["deeper"].(bool)

	result := func(v interface{}, msg string) (*FeatureResult, error) {
		return &FeatureResult{
			Success: true,
			Data:    map[string]interface{}{"view": v},
			Message: msg,
		}, nil
	}

	switch view {
	case "families":
		search := ""
		if s, ok := params["search"].(string); ok {
			search = strings.ToLower(strings.TrimSpace(s))
		}
		limit := 10
		if l, ok := params["limit"].(float64); ok {
			limit = int(l)
			if limit > 50 {
				limit = 50
			}
			if limit < 1 {
				limit = 1
			}
		}
		data := familiesData(apiProvider, search, person, limit, viewOffset(params))
		return result(data, fmt.Sprintf("families view: %d of %d families", data.Shown, data.TotalFamilies))

	case "person":
		if person == "" {
			return &FeatureResult{Success: false, Message: "The person view needs person='<@handle, name, or user ID>'"}, nil
		}
		pv := personView(ctx, apiProvider, person, viewDays(params, 30))
		pv.Offset = viewOffset(params)
		return result(pv, "person view")

	case "initiatives":
		iv := initiativesView(apiProvider, viewDays(params, 7))
		iv.Offset = viewOffset(params)
		return result(iv, "initiatives view")

	case "convergence":
		raw, _ := params["people"].(string)
		people := strings.Split(raw, ",")
		if len(people) < 2 {
			return &FeatureResult{Success: false, Message: "The convergence view needs people='a,b[,c...]' — at least two"}, nil
		}
		cv := convergenceView(ctx, apiProvider, people, viewDays(params, 14))
		cv.Offset = viewOffset(params)
		return result(cv, "convergence view")

	case "about":
		if person == "" {
			return &FeatureResult{Success: false, Message: "The about view needs person='<@handle, name, or user ID>'"}, nil
		}
		return result(aboutView(ctx, apiProvider, person, viewDays(params, 30), deeper), "about view")

	default:
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("Unknown view %q. Available views: about, families, person, initiatives, convergence", view),
			Guidance: "Start with view='about' person='<who>' — it composes the others and returns a reading plan.",
		}, nil
	}
}

// familiesData executes the families view: stem grouping ⋈ lifecycle ⋈
// creator, from local state only.
func familiesData(apiProvider *provider.ApiProvider, search, person string, limit, offset int) *estateFamiliesData {
	data := &estateFamiliesData{Search: search, CreatorNames: map[string]string{}, Offset: offset, Limit: limit}
	data.Coverage = apiProvider.EstateCoverage()
	data.Attention = apiProvider.AttentionInfo()

	var personID string
	if person != "" {
		res := apiProvider.ResolvePerson(person)
		if !res.Resolved {
			data.PersonMiss = &res
			return data
		}
		personID = res.UserID
		data.PersonLabel = res.DisplayName
		if data.PersonLabel == "" {
			data.PersonLabel = res.Handle
		}
	}

	// Merge the two sources, keyed by conversation ID so a reused name
	// never grafts a dead channel's ownership onto a live one — the two
	// render side by side in their stem group. The snapshot carries
	// ownership today; the fold carries it from the next sweep on, and is
	// the only source that still knows the channels that are gone.
	channels := map[string]famChannel{}
	for _, ch := range apiProvider.GetCachedChannels() {
		if ch.IsIM || ch.IsMpIM || ch.Name == "" {
			continue
		}
		channels[ch.ID] = famChannel{
			Name:     ch.Name,
			Creator:  ch.Creator,
			Created:  int64(ch.Created),
			Archived: ch.IsArchived,
		}
	}
	for id, rec := range apiProvider.EstateChannels() {
		p := rec.Props
		if p.IsIM || p.IsMpim || p.Name == "" {
			continue
		}
		if p.Creator != "" {
			data.FoldOwned++
		}
		if existing, ok := channels[id]; ok {
			if existing.Creator == "" && p.Creator != "" {
				existing.Creator = p.Creator
				existing.Created = p.Created
				channels[id] = existing
			}
			continue
		}
		if rec.Gone == nil {
			continue
		}
		data.GoneIncluded++
		channels[id] = famChannel{
			Name:     p.Name,
			Creator:  p.Creator,
			Created:  p.Created,
			Archived: p.IsArchived,
			GoneFrom: rec.Gone.NotBefore,
			GoneTo:   rec.Gone.At,
		}
	}
	data.Considered = len(channels)

	groups := map[string][]famChannel{}
	for _, ch := range channels {
		stem := strings.SplitN(ch.Name, "-", 2)[0]
		groups[stem] = append(groups[stem], ch)
	}

	relaxed := search != "" || personID != ""
	var fams []famGroup
	for stem, chs := range groups {
		if search != "" {
			match := strings.Contains(stem, search)
			for _, c := range chs {
				if strings.Contains(strings.ToLower(c.Name), search) {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		if personID != "" {
			created := false
			for _, c := range chs {
				if c.Creator == personID {
					created = true
				}
			}
			if !created {
				continue
			}
		}
		f := famGroup{Stem: stem, Channels: chs, From: 1 << 62}
		for _, c := range chs {
			toks := strings.Split(c.Name, "-")
			if famPhaseTokens[toks[len(toks)-1]] {
				f.Phased++
			}
			if c.Created > 0 && c.Created < f.From {
				f.From = c.Created
			}
			if c.Created > f.To {
				f.To = c.Created
			}
		}
		if !relaxed && (len(chs) < 2 || f.Phased == 0) {
			continue
		}
		sort.Slice(f.Channels, func(i, j int) bool {
			if f.Channels[i].Created != f.Channels[j].Created {
				return f.Channels[i].Created < f.Channels[j].Created
			}
			return f.Channels[i].Name < f.Channels[j].Name
		})
		fams = append(fams, f)
	}
	sort.Slice(fams, func(i, j int) bool {
		if len(fams[i].Channels) != len(fams[j].Channels) {
			return len(fams[i].Channels) > len(fams[j].Channels)
		}
		return fams[i].Stem < fams[j].Stem
	})

	data.TotalFamilies = len(fams)
	if offset >= len(fams) {
		fams = nil
	} else {
		fams = fams[offset:]
	}
	if len(fams) > limit {
		fams = fams[:limit]
	}
	data.Families = fams
	data.Shown = len(fams)

	// Resolve creator names once through the shared chain: users map, then
	// the estate fold with a departed marker, then the labelled-unknown
	// fallback — every view renders an unknown user the same way.
	for _, f := range fams {
		for _, c := range f.Channels {
			if c.Creator == "" {
				continue
			}
			if _, done := data.CreatorNames[c.Creator]; done {
				continue
			}
			data.CreatorNames[c.Creator] = userLabel(apiProvider, c.Creator)
		}
	}
	return data
}

// formatEstate renders the estate views. The rendered markdown is the
// agent's entire world — result.Data never reaches the client.
func formatEstate(result *FeatureResult) string {
	dataMap, _ := result.Data.(map[string]interface{})
	switch v := dataMap["view"].(type) {
	case *estateFamiliesData:
		return formatFamiliesView(v)
	case *personViewData:
		return formatPersonView(v)
	case *initiativesViewData:
		return formatInitiativesView(v)
	case *convergenceViewData:
		return formatConvergenceView(v)
	case *aboutViewData:
		return formatAboutView(v)
	default:
		out := result.Message
		if result.Guidance != "" {
			out += "\n\n" + result.Guidance
		}
		return out
	}
}

func formatFamiliesView(view *estateFamiliesData) string {
	var b strings.Builder

	if view.PersonMiss != nil {
		b.WriteString("## Estate — families view\n\n")
		renderMiss(&b, view.PersonMiss)
		return b.String()
	}

	title := "## Estate — families view"
	if view.PersonLabel != "" {
		title += fmt.Sprintf(" — created by %s", view.PersonLabel)
	}
	if view.Search != "" {
		title += fmt.Sprintf(" — matching %q", view.Search)
	}
	b.WriteString(title + "\n\n")

	criteria := "≥2 channels, ≥1 phase-tagged"
	if view.Search != "" || view.PersonLabel != "" {
		criteria = "any matching group"
	}
	fmt.Fprintf(&b, "%d families (%s) from %d named channels", view.TotalFamilies, criteria, view.Considered)
	if view.GoneIncluded > 0 {
		fmt.Fprintf(&b, ", %d of them gone channels only the estate still remembers", view.GoneIncluded)
	}
	if view.Offset > 0 || view.Offset+view.Shown < view.TotalFamilies {
		fmt.Fprintf(&b, "; showing %d–%d", view.Offset+1, view.Offset+view.Shown)
	}
	b.WriteString(".\n\n")

	for _, f := range view.Families {
		span := ""
		if f.From < 1<<62 && f.From > 0 {
			span = fmt.Sprintf(", opened %s → %s",
				time.Unix(f.From, 0).Format("2006-01"), time.Unix(f.To, 0).Format("2006-01"))
		}
		fmt.Fprintf(&b, "### %s — %d channels%s\n", f.Stem, len(f.Channels), span)
		for _, c := range f.Channels {
			line := "- #" + c.Name
			if c.Created > 0 {
				line += "  created " + time.Unix(c.Created, 0).Format("2006-01-02")
			}
			if c.Creator != "" {
				line += " by " + view.CreatorNames[c.Creator]
			}
			switch {
			case !c.GoneTo.IsZero():
				line += fmt.Sprintf("  [gone %s…%s]",
					c.GoneFrom.Format("2006-01-02"), c.GoneTo.Format("2006-01-02"))
			case c.Archived:
				line += "  [archived]"
			default:
				line += "  [live]"
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("---\n")
	cov := "Coverage: fold executor, 0 Slack calls"
	if view.Coverage.Available {
		cov += fmt.Sprintf("; estate holds %d channels", view.Coverage.Channels)
		if !view.Coverage.LastFullSweep.IsZero() {
			cov += fmt.Sprintf(", last full sweep %s", view.Coverage.LastFullSweep.Format("2006-01-02 15:04"))
		}
	} else {
		cov += "; estate unavailable — served from the session snapshot alone"
	}
	if view.FoldOwned == 0 {
		cov += ". Ownership read from the live snapshot; the estate records it from the next sweep on"
	}
	b.WriteString(cov + ".\n")
	att := view.Attention
	if !att.Available {
		b.WriteString("Activity plane: no attention ledger — creation facts only, activity views pending.\n")
	} else if att.Stats.Events == 0 {
		b.WriteString("Activity plane: observing — no encounters recorded yet.\n")
	} else {
		fmt.Fprintf(&b, "Activity plane: %d encounters, %d people, %d conversations (%s → %s), as observed by this agent's reading.\n",
			att.Stats.Events, att.Stats.Users, att.Stats.Convs, att.Stats.FirstDay, att.Stats.LastDay)
	}
	if view.Offset+view.Shown < view.TotalFamilies {
		next := fmt.Sprintf("estate view='families' offset=%d", view.Offset+view.Shown)
		if view.Search != "" {
			next += fmt.Sprintf(" search='%s'", view.Search)
		}
		fmt.Fprintf(&b, "**Next page:** %d more families — %s\n", view.TotalFamilies-view.Offset-view.Shown, next)
	}
	b.WriteString("**Next:** whole picture: estate view='about' person='@<handle>' | read a channel: catch-up channel='#<name>' | narrow: estate view='families' search='<stem>'")

	return b.String()
}
