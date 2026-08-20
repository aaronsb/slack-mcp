// Command estate-experiment is a throwaway probe (prototype-before-accept)
// for ADR-008's load-bearing claims. It answers, against the real workspace:
//
//	A. Does stem-grouping over the cached channel snapshot produce coherent
//	   engagement families with creators and lifecycle dates? (fold executor,
//	   local data only, zero API calls)
//	B. Does the compiled executor — a bounded set of `search from:@handle
//	   after:date` queries — reconstruct activity strips good enough for the
//	   convergence motif, and at what API cost?
//	C. Person-view rankings from the same data: top conversations and the
//	   hour-cadence parallelism distribution.
//
// It writes a report to stdout and changes nothing: reads the snapshot
// files directly and talks to Slack read-only. Delete after ADR-008's
// implementation cites its numbers.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/paths"
	"github.com/aaronsb/slack-mcp/pkg/transport"
	"github.com/slack-go/slack"
)

var phaseTokens = map[string]bool{
	"sales": true, "assessment": true, "implementation": true,
	"training": true, "support": true, "delivery": true,
	"migration": true, "onboarding": true, "discovery": true,
}

type channelLite struct {
	ID, Name, Creator string
	Created           int64
	IsArchived        bool
	IsIM, IsMpim      bool
}

func main() {
	people := flag.String("people", "", "comma-separated handles for the convergence probe (required for the live part)")
	days := flag.Int("days", 14, "window for the live probes")
	pages := flag.Int("pages", 3, "max search pages per person (100 matches each)")
	flag.Parse()

	channels, users := loadSnapshots()
	fmt.Printf("# ADR-008 evidence probe — %s\n\n", time.Now().Format("2006-01-02 15:04"))

	probeFamilies(channels, users)
	if *people == "" {
		fmt.Println("(live probe skipped: pass -people handle1,handle2,... to run it)")
		return
	}
	handles := strings.Split(*people, ",")
	for i := range handles {
		handles[i] = strings.TrimSpace(handles[i])
	}
	probeLive(handles, *days, *pages, users)
}

// --- Part A: the families motif, fold executor, local data only ---

func probeFamilies(channels []channelLite, users map[string]slack.User) {
	families := map[string][]channelLite{}
	realChannels := 0
	withCreator := 0
	for _, c := range channels {
		if c.IsIM || c.IsMpim || c.Name == "" {
			continue
		}
		realChannels++
		if c.Creator != "" {
			withCreator++
		}
		stem := strings.SplitN(c.Name, "-", 2)[0]
		families[stem] = append(families[stem], c)
	}

	type fam struct {
		stem     string
		channels []channelLite
		phased   int
		span     [2]int64
	}
	var fams []fam
	for stem, chs := range families {
		if len(chs) < 2 {
			continue
		}
		f := fam{stem: stem, channels: chs}
		f.span[0] = 1 << 62
		for _, c := range chs {
			toks := strings.Split(c.Name, "-")
			if phaseTokens[toks[len(toks)-1]] {
				f.phased++
			}
			if c.Created > 0 && c.Created < f.span[0] {
				f.span[0] = c.Created
			}
			if c.Created > f.span[1] {
				f.span[1] = c.Created
			}
		}
		if f.phased == 0 {
			continue
		}
		fams = append(fams, f)
	}
	sort.Slice(fams, func(i, j int) bool { return len(fams[i].channels) > len(fams[j].channels) })

	fmt.Printf("## A. Families (local only, 0 API calls)\n\n")
	pct := 0.0
	if realChannels > 0 {
		pct = 100 * float64(withCreator) / float64(realChannels)
	}
	fmt.Printf("- channels considered: %d (creator present: %d, %.0f%%)\n", realChannels, withCreator, pct)
	fmt.Printf("- engagement families found (>=2 channels, >=1 phase-tagged): %d\n\n", len(fams))

	show := 12
	if len(fams) < show {
		show = len(fams)
	}
	for _, f := range fams[:show] {
		fmt.Printf("### %s — %d channels, opened %s → %s\n", f.stem, len(f.channels),
			time.Unix(f.span[0], 0).Format("2006-01"), time.Unix(f.span[1], 0).Format("2006-01"))
		sort.Slice(f.channels, func(i, j int) bool { return f.channels[i].Created < f.channels[j].Created })
		for _, c := range f.channels {
			state := "live"
			if c.IsArchived {
				state = "archived"
			}
			fmt.Printf("  - #%s  created %s by %s  [%s]\n", c.Name,
				time.Unix(c.Created, 0).Format("2006-01-02"), userName(users, c.Creator), state)
		}
		fmt.Println()
	}
}

// --- Part B/C: the compiled executor — bounded live searches ---

func probeLive(handles []string, days, maxPages int, users map[string]slack.User) {
	client, err := newClient()
	if err != nil {
		fmt.Printf("## B. Live probe skipped: %v\n", err)
		return
	}
	after := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	start := time.Now()
	calls := 0

	// person -> channelName -> day -> count; and hour buckets for handles[0]
	activity := map[string]map[string]map[string]int{}
	hourBuckets := map[string]map[string]bool{} // hour -> set of conversations
	truncated := map[string]int{}

	for _, h := range handles {
		h = strings.TrimSpace(h)
		activity[h] = map[string]map[string]int{}
		query := fmt.Sprintf("from:@%s after:%s", h, after)
		page := 1
		for {
			params := slack.NewSearchParameters()
			params.Count = 100
			params.Page = page
			params.Sort = "timestamp"
			msgs, err := client.SearchMessagesContext(context.Background(), query, params)
			calls++
			if err != nil {
				fmt.Printf("search %q failed: %v\n", query, err)
				break
			}
			for _, m := range msgs.Matches {
				conv := m.Channel.Name
				if conv == "" {
					conv = m.Channel.ID
				}
				day := slackDay(m.Timestamp)
				if activity[h][conv] == nil {
					activity[h][conv] = map[string]int{}
				}
				activity[h][conv][day]++
				if h == handles[0] {
					hr := slackHour(m.Timestamp)
					if hourBuckets[hr] == nil {
						hourBuckets[hr] = map[string]bool{}
					}
					hourBuckets[hr][conv] = true
				}
			}
			if page >= maxPages || page*100 >= msgs.Total {
				if msgs.Total > page*100 {
					truncated[h] = msgs.Total - page*100
				}
				break
			}
			page++
			time.Sleep(1200 * time.Millisecond)
		}
		time.Sleep(1200 * time.Millisecond)
	}

	fmt.Printf("## B. Convergence motif (compiled executor, live)\n\n")
	fmt.Printf("- window: last %d days; people: %v\n", days, handles)
	fmt.Printf("- API cost: %d search calls in %s\n", calls, time.Since(start).Round(time.Millisecond))
	for h, n := range truncated {
		fmt.Printf("- coverage: %s truncated, %d matches beyond page cap\n", h, n)
	}
	fmt.Println()

	// (conversation, day) -> people present
	type cell struct{ conv, day string }
	cells := map[cell][]string{}
	for h, convs := range activity {
		for conv, dayCounts := range convs {
			for day := range dayCounts {
				c := cell{conv, day}
				cells[c] = append(cells[c], h)
			}
		}
	}
	type cluster struct {
		conv, day string
		people    []string
	}
	var clusters []cluster
	for c, ppl := range cells {
		if len(ppl) >= 2 {
			sort.Strings(ppl)
			clusters = append(clusters, cluster{c.conv, c.day, ppl})
		}
	}
	sort.Slice(clusters, func(i, j int) bool {
		if len(clusters[i].people) != len(clusters[j].people) {
			return len(clusters[i].people) > len(clusters[j].people)
		}
		return clusters[i].day > clusters[j].day
	})
	fmt.Printf("co-activity cells (same conversation, same day, >=2 of them): %d\n\n", len(clusters))
	for i, cl := range clusters {
		if i >= 15 {
			fmt.Printf("  … and %d more\n", len(clusters)-15)
			break
		}
		fmt.Printf("  - %s  %s  ← %s\n", cl.day, cl.conv, strings.Join(cl.people, ", "))
	}

	// C1: top conversations for handles[0]
	fmt.Printf("\n## C. Person view for @%s (same data, 0 extra calls)\n\n", handles[0])
	type rank struct {
		conv string
		n    int
	}
	var ranks []rank
	for conv, days := range activity[handles[0]] {
		total := 0
		for _, n := range days {
			total += n
		}
		ranks = append(ranks, rank{conv, total})
	}
	sort.Slice(ranks, func(i, j int) bool { return ranks[i].n > ranks[j].n })
	fmt.Printf("top conversations by message volume (last %d days):\n", days)
	for i, r := range ranks {
		if i >= 10 {
			break
		}
		fmt.Printf("  %2d. %-40s %d messages\n", i+1, r.conv, r.n)
	}

	// C2: hour-cadence parallelism
	dist := map[int]int{} // concurrent conversations -> hours
	peak := 0
	peakHour := ""
	for hr, convs := range hourBuckets {
		dist[len(convs)]++
		if len(convs) > peak {
			peak = len(convs)
			peakHour = hr
		}
	}
	fmt.Printf("\nhour-cadence parallelism (distinct conversations per active hour):\n")
	var ks []int
	for k := range dist {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	for _, k := range ks {
		fmt.Printf("  %d conversation(s): %d hours\n", k, dist[k])
	}
	if peakHour != "" {
		fmt.Printf("  peak: %d concurrent conversations at %s: %s\n", peak, peakHour,
			strings.Join(keys(hourBuckets[peakHour]), ", "))
	}
	_ = users
}

// --- plumbing ---

func loadSnapshots() ([]channelLite, map[string]slack.User) {
	dir := paths.DataDir()
	var raw []map[string]interface{}
	data, err := os.ReadFile(filepath.Join(dir, "channels.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "no channel snapshot: %v\n", err)
		os.Exit(1)
	}
	_ = json.Unmarshal(data, &raw)
	channels := make([]channelLite, 0, len(raw))
	for _, m := range raw {
		c := channelLite{}
		c.ID, _ = m["id"].(string)
		c.Name, _ = m["name"].(string)
		c.Creator, _ = m["creator"].(string)
		if f, ok := m["created"].(float64); ok {
			c.Created = int64(f)
		}
		c.IsArchived, _ = m["is_archived"].(bool)
		c.IsIM, _ = m["is_im"].(bool)
		c.IsMpim, _ = m["is_mpim"].(bool)
		channels = append(channels, c)
	}

	users := map[string]slack.User{}
	var ulist []slack.User
	if data, err := os.ReadFile(filepath.Join(dir, "users.json")); err == nil {
		_ = json.Unmarshal(data, &ulist)
		for _, u := range ulist {
			users[u.ID] = u
		}
	}
	return channels, users
}

func newClient() (*slack.Client, error) {
	cfg := struct {
		Workspaces map[string]struct {
			Xoxc string `json:"xoxc_token"`
			Xoxd string `json:"xoxd_token"`
		} `json:"workspaces"`
		Default string `json:"default_workspace"`
	}{}
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".config", "slack-mcp", "config.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	ws, ok := cfg.Workspaces[cfg.Default]
	if !ok {
		return nil, fmt.Errorf("no default workspace in config")
	}
	client := &http.Client{Transport: transport.New(http.DefaultTransport,
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		ws.Xoxd)}
	return slack.New(ws.Xoxc, slack.OptionHTTPClient(client)), nil
}

func userName(users map[string]slack.User, id string) string {
	if u, ok := users[id]; ok {
		if u.RealName != "" {
			return u.RealName
		}
		return u.Name
	}
	if id == "" {
		return "(unknown)"
	}
	return id
}

func slackDay(ts string) string {
	return slackTime(ts).Format("2006-01-02")
}

func slackHour(ts string) string {
	return slackTime(ts).Format("2006-01-02 15:00")
}

func slackTime(ts string) time.Time {
	sec := strings.SplitN(ts, ".", 2)[0]
	n, _ := strconv.ParseInt(sec, 10, 64)
	return time.Unix(n, 0)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
