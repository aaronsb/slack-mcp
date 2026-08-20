package features

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// The activity-plane views (ADR-008 stage 3). Every view is fold-first; the
// compiled executor runs only when the question exceeds the fold's coverage,
// and its observations land in the attention ledger so the joins always run
// in one place. Views report observations, never judgments.

const stripGapDays = 1

// convLabels builds the conversation-ID -> label map once per view: channel
// names, and IM conversations resolved through the COUNTERPART join — Slack
// search names IMs by raw counterpart ID, so this join is mandatory.
type convInfo struct {
	Label string
	IsIM  bool
	// Counterpart is the other user's ID for IM conversations.
	Counterpart string
}

func convLabels(ap *provider.ApiProvider) map[string]convInfo {
	users := ap.ProvideUsersMap()
	out := map[string]convInfo{}
	for _, ch := range ap.GetCachedChannels() {
		switch {
		case ch.IsIM:
			label := ch.User
			if u, ok := users[ch.User]; ok {
				label = "@" + displayName(u)
			}
			out[ch.ID] = convInfo{Label: "DM " + label, IsIM: true, Counterpart: ch.User}
		case ch.IsMpIM:
			out[ch.ID] = convInfo{Label: groupName(ch.Name, nil)}
		case ch.Name != "":
			out[ch.ID] = convInfo{Label: "#" + ch.Name}
		}
	}
	// The estate fold still names conversations the snapshot has dropped —
	// activity in a since-deleted channel renders as a dated fact, never as
	// a raw internal ID.
	for id, rec := range ap.EstateChannels() {
		if _, ok := out[id]; ok {
			continue
		}
		p := rec.Props
		switch {
		case p.IsIM:
			out[id] = convInfo{Label: "DM " + p.User, IsIM: true, Counterpart: p.User}
		case p.Name != "":
			label := "#" + p.Name
			if rec.Gone != nil {
				label += " (gone)"
			}
			out[id] = convInfo{Label: label}
		}
	}
	return out
}

func labelFor(labels map[string]convInfo, conv string) string {
	if info, ok := labels[conv]; ok {
		return info.Label
	}
	return conv
}

func userLabel(ap *provider.ApiProvider, id string) string {
	if u, ok := ap.ProvideUsersMap()[id]; ok {
		return displayName(u)
	}
	if rec, ok := ap.EstateUser(id); ok {
		name := rec.Props.RealName
		if name == "" {
			name = rec.Props.Name
		}
		if rec.Gone != nil {
			name += " (departed)"
		}
		return name
	}
	return id
}

func sinceDay(days int) string {
	return time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
}

func windowed(encs []estate.Encounter, since string) []estate.Encounter {
	var out []estate.Encounter
	for _, e := range encs {
		if e.Day >= since {
			out = append(out, e)
		}
	}
	return out
}

// resolvePeople runs the ladder over each input. Misses come back
// separately so views render candidates instead of failing.
func resolvePeople(ap *provider.ApiProvider, inputs []string) (map[string]string, map[string]string, []provider.PersonResolution) {
	ids := map[string]string{}    // id -> handle
	labels := map[string]string{} // id -> display label
	var misses []provider.PersonResolution
	for _, in := range inputs {
		in = strings.TrimSpace(in)
		if in == "" {
			continue
		}
		res := ap.ResolvePerson(in)
		if !res.Resolved {
			misses = append(misses, res)
			continue
		}
		ids[res.UserID] = res.Handle
		label := res.DisplayName
		if label == "" {
			label = res.Handle
		}
		labels[res.UserID] = label
	}
	return ids, labels, misses
}

// ensureCoverage compiles activity for the people whose window has no
// observed encounters, when a writable attention ledger exists to receive
// the observations. Returns stats when the compiled executor ran.
func ensureCoverage(ctx context.Context, ap *provider.ApiProvider, ids map[string]string, since string, days, pages int) *provider.CompiledStats {
	if !ap.AttentionWritable() {
		// No writable ledger to receive the observations; compiling would
		// discard its own results. A read-only instance answers from the
		// fold and says so in coverage.
		return nil
	}
	need := map[string]string{}
	for id, handle := range ids {
		if len(windowedByConv(ap.UserEncounters(id), since)) == 0 {
			need[id] = handle
		}
	}
	if len(need) == 0 {
		return nil
	}
	stats := ap.CompileActivity(ctx, need, days, pages)
	return &stats
}

func windowedByConv(byConv map[string][]estate.Encounter, since string) map[string][]estate.Encounter {
	out := map[string][]estate.Encounter{}
	for conv, encs := range byConv {
		if w := windowed(encs, since); len(w) > 0 {
			out[conv] = w
		}
	}
	return out
}

// ---- person view ----

type footprintRow struct {
	Conv   string
	Days   int
	Strips []estate.Strip
}

type counterpartRow struct {
	ID     string
	CoDays int
	DMDays int
}

type personViewData struct {
	Label, Title string
	Days         int
	Footprint    []footprintRow
	Counterparts []counterpartRow
	Parallelism  map[int]int // concurrent conversations -> hours, self only
	Peak         int
	Created      []string // channel names created by this person
	Coverage     provider.EstateCoverageInfo
	Attention    provider.AttentionInfo
	Compiled     *provider.CompiledStats
	Labels       map[string]convInfo
	Names        map[string]string
	Miss         *provider.PersonResolution
	Handle       string
}

func personView(ctx context.Context, ap *provider.ApiProvider, person string, days int) *personViewData {
	data := &personViewData{Days: days}
	ids, labels, misses := resolvePeople(ap, []string{person})
	if len(misses) > 0 {
		data.Miss = &misses[0]
		return data
	}
	var id string
	for i := range ids {
		id = i
	}
	data.Label = labels[id]
	data.Handle = ids[id]
	if u, ok := ap.ProvideUsersMap()[id]; ok {
		data.Title = u.Profile.Title
	}

	since := sinceDay(days)
	data.Compiled = ensureCoverage(ctx, ap, ids, since, days, 3)

	byConv := windowedByConv(ap.UserEncounters(id), since)
	for conv, encs := range byConv {
		data.Footprint = append(data.Footprint, footprintRow{
			Conv:   conv,
			Days:   len(estate.ActiveDays(encs, since)),
			Strips: estate.Strips(encs, stripGapDays),
		})
	}
	sort.Slice(data.Footprint, func(i, j int) bool {
		if data.Footprint[i].Days != data.Footprint[j].Days {
			return data.Footprint[i].Days > data.Footprint[j].Days
		}
		return data.Footprint[i].Conv < data.Footprint[j].Conv
	})

	data.Labels = convLabels(ap)
	data.Names = map[string]string{}

	// Counterparts: distinct co-active days in shared conversations, DM
	// days counted through the COUNTERPART join. Day sets, not increments —
	// the same calendar day shared across three channels is one co-active
	// day, and a DM day where both sides were observed counts once.
	coDays := map[string]map[string]struct{}{}
	dmDays := map[string]map[string]struct{}{}
	mark := func(m map[string]map[string]struct{}, u, day string) {
		if m[u] == nil {
			m[u] = map[string]struct{}{}
		}
		m[u][day] = struct{}{}
	}
	plane := ap.AttentionByConversation()
	for conv := range byConv {
		own := map[string]struct{}{}
		for _, d := range estate.ActiveDays(byConv[conv], since) {
			own[d] = struct{}{}
		}
		isIM := data.Labels[conv].IsIM
		counterpart := data.Labels[conv].Counterpart
		for day, users := range plane[conv] {
			if day < since {
				continue
			}
			if _, active := own[day]; !active {
				continue
			}
			counterpartSeen := false
			for _, u := range users {
				if u == id {
					continue
				}
				mark(coDays, u, day)
				if u == counterpart {
					counterpartSeen = true
				}
			}
			// An IM day where only the seed's side was observed still
			// belongs to the counterpart; a both-observed day already
			// counted above.
			if isIM && counterpart != "" && counterpart != id && !counterpartSeen {
				mark(dmDays, counterpart, day)
			}
		}
	}
	cpIDs := map[string]struct{}{}
	for u := range coDays {
		cpIDs[u] = struct{}{}
	}
	for u := range dmDays {
		cpIDs[u] = struct{}{}
	}
	for u := range cpIDs {
		data.Counterparts = append(data.Counterparts, counterpartRow{
			ID: u, CoDays: len(coDays[u]), DMDays: len(dmDays[u]),
		})
	}
	sort.Slice(data.Counterparts, func(i, j int) bool {
		ti := data.Counterparts[i].CoDays + data.Counterparts[i].DMDays
		tj := data.Counterparts[j].CoDays + data.Counterparts[j].DMDays
		if ti != tj {
			return ti > tj
		}
		return data.Counterparts[i].ID < data.Counterparts[j].ID
	})
	for _, row := range data.Counterparts {
		data.Names[row.ID] = userLabel(ap, row.ID)
	}

	// Founder facts from the estate.
	for _, ch := range ap.GetCachedChannels() {
		if ch.Creator == id && !ch.IsIM && !ch.IsMpIM && ch.Name != "" {
			data.Created = append(data.Created, ch.Name)
		}
	}
	sort.Strings(data.Created)

	// Hour-cadence parallelism, operator only.
	if id == ap.SelfUserID() {
		hours := map[string]map[string]struct{}{}
		for conv, encs := range byConv {
			for _, e := range encs {
				if e.Hour == nil {
					continue
				}
				k := fmt.Sprintf("%s %02d", e.Day, *e.Hour)
				if hours[k] == nil {
					hours[k] = map[string]struct{}{}
				}
				hours[k][conv] = struct{}{}
			}
		}
		data.Parallelism = map[int]int{}
		for _, convs := range hours {
			n := len(convs)
			data.Parallelism[n]++
			if n > data.Peak {
				data.Peak = n
			}
		}
	}

	data.Coverage = ap.EstateCoverage()
	data.Attention = ap.AttentionInfo()
	return data
}

// ---- initiatives view ----

type initiativeChannel struct {
	Conv       string
	ActiveDays int
	People     int
}

type initiativeRow struct {
	Creator        string
	CreatorActive  bool
	Channels       []initiativeChannel
	TotalActiveDay int
}

type initiativesViewData struct {
	Days      int
	Rows      []initiativeRow
	Names     map[string]string
	Labels    map[string]convInfo
	Coverage  provider.EstateCoverageInfo
	Attention provider.AttentionInfo
}

func initiativesView(ap *provider.ApiProvider, days int) *initiativesViewData {
	data := &initiativesViewData{Days: days, Names: map[string]string{}}
	since := sinceDay(days)
	plane := ap.AttentionByConversation()
	data.Labels = convLabels(ap)

	creatorByConv := map[string]string{}
	for _, ch := range ap.GetCachedChannels() {
		if !ch.IsIM && !ch.IsMpIM && ch.Creator != "" {
			creatorByConv[ch.ID] = ch.Creator
		}
	}
	// The estate fold supplies creators the snapshot no longer holds, so a
	// channel deleted mid-window keeps its movement attributed.
	for id, rec := range ap.EstateChannels() {
		p := rec.Props
		if _, ok := creatorByConv[id]; !ok && !p.IsIM && !p.IsMpim && p.Creator != "" {
			creatorByConv[id] = p.Creator
		}
	}

	byCreator := map[string]*initiativeRow{}
	for conv, daysMap := range plane {
		creator := creatorByConv[conv]
		if creator == "" {
			continue
		}
		active := 0
		people := map[string]struct{}{}
		creatorSeen := false
		for day, users := range daysMap {
			if day < since {
				continue
			}
			active++
			for _, u := range users {
				people[u] = struct{}{}
				if u == creator {
					creatorSeen = true
				}
			}
		}
		if active == 0 {
			continue
		}
		row, ok := byCreator[creator]
		if !ok {
			row = &initiativeRow{Creator: creator}
			byCreator[creator] = row
		}
		row.Channels = append(row.Channels, initiativeChannel{Conv: conv, ActiveDays: active, People: len(people)})
		row.TotalActiveDay += active
		row.CreatorActive = row.CreatorActive || creatorSeen
	}

	for _, row := range byCreator {
		sort.Slice(row.Channels, func(i, j int) bool { return row.Channels[i].ActiveDays > row.Channels[j].ActiveDays })
		data.Rows = append(data.Rows, *row)
		data.Names[row.Creator] = userLabel(ap, row.Creator)
	}
	sort.Slice(data.Rows, func(i, j int) bool {
		if data.Rows[i].TotalActiveDay != data.Rows[j].TotalActiveDay {
			return data.Rows[i].TotalActiveDay > data.Rows[j].TotalActiveDay
		}
		return data.Rows[i].Creator < data.Rows[j].Creator
	})

	data.Coverage = ap.EstateCoverage()
	data.Attention = ap.AttentionInfo()
	return data
}

// ---- convergence view ----

type convergenceCell struct {
	Conv   string
	CoDays int
	First  string
	Last   string
	Seen   []string // member IDs observed there
}

type convergenceViewData struct {
	Days      int
	People    map[string]string // id -> label
	Baseline  map[string]int    // id -> active days in window
	Cells     []convergenceCell
	Misses    []provider.PersonResolution
	Compiled  *provider.CompiledStats
	Labels    map[string]convInfo
	Coverage  provider.EstateCoverageInfo
	Attention provider.AttentionInfo
}

func convergenceView(ctx context.Context, ap *provider.ApiProvider, people []string, days int) *convergenceViewData {
	data := &convergenceViewData{Days: days}
	ids, labels, misses := resolvePeople(ap, people)
	data.Misses = misses
	data.People = labels
	if len(ids) < 2 {
		return data
	}

	since := sinceDay(days)
	data.Compiled = ensureCoverage(ctx, ap, ids, since, days, 2)
	data.Labels = convLabels(ap)

	// Per-person active days per conversation.
	perConv := map[string]map[string]map[string]struct{}{} // conv -> day -> ids present
	data.Baseline = map[string]int{}
	for id := range ids {
		byConv := windowedByConv(ap.UserEncounters(id), since)
		total := map[string]struct{}{}
		for conv, encs := range byConv {
			for _, d := range estate.ActiveDays(encs, since) {
				total[d] = struct{}{}
				if perConv[conv] == nil {
					perConv[conv] = map[string]map[string]struct{}{}
				}
				if perConv[conv][d] == nil {
					perConv[conv][d] = map[string]struct{}{}
				}
				perConv[conv][d][id] = struct{}{}
			}
		}
		data.Baseline[id] = len(total)
	}

	for conv, daysMap := range perConv {
		cell := convergenceCell{Conv: conv}
		members := map[string]struct{}{}
		for day, present := range daysMap {
			if len(present) < 2 {
				continue
			}
			cell.CoDays++
			if cell.First == "" || day < cell.First {
				cell.First = day
			}
			if day > cell.Last {
				cell.Last = day
			}
			for id := range present {
				members[id] = struct{}{}
			}
		}
		if cell.CoDays == 0 {
			continue
		}
		for id := range members {
			cell.Seen = append(cell.Seen, id)
		}
		sort.Strings(cell.Seen)
		data.Cells = append(data.Cells, cell)
	}
	sort.Slice(data.Cells, func(i, j int) bool {
		if data.Cells[i].CoDays != data.Cells[j].CoDays {
			return data.Cells[i].CoDays > data.Cells[j].CoDays
		}
		return data.Cells[i].Conv < data.Cells[j].Conv
	})

	data.Coverage = ap.EstateCoverage()
	data.Attention = ap.AttentionInfo()
	return data
}
