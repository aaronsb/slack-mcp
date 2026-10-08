package features

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// Send times for scheduled send (ADR-016): parsing at=, the zones a time
// is read and rendered in, and the clock tests pin.

// The bounds a send time must fall in. Slack refused 60 seconds out about
// half the time in the probe; two minutes sits above its floor. 120 days is
// Slack's own limit.
const (
	scheduleMinLead = 2 * time.Minute
	scheduleMaxLead = 120 * 24 * time.Hour
)

// naiveLayouts are the accepted times with no offset.
var naiveLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
}

// atForms names the accepted forms in every parse refusal.
const atForms = "RFC 3339 with an offset or Z ('2026-10-02T09:00:00-06:00'), a date and time with no offset ('2026-10-02T09:00', read in your Slack profile zone), or Unix seconds"

// atString is the at= parameter as the caller passed it, for the echo and
// the parser: a JSON number arrives as float64.
func atString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == math.Trunc(t) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// parseAt resolves a send time. A time with no offset is read in profile
// (the person's Slack profile zone) only when system reads it as the same
// instant; otherwise it is refused, naming both zones. profile is nil when
// the profile zone is unknown, which refuses every time with no offset. The
// result is bounds-checked against now.
func parseAt(s string, now time.Time, profile, system *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("at= is empty. Accepted: " + atForms + ".")
	}
	var at time.Time
	if isDigits(s) {
		secs, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("at=%q is not a time. Accepted: %s.", s, atForms)
		}
		at = time.Unix(secs, 0)
	} else if t, err := time.Parse(time.RFC3339, s); err == nil {
		at = t
	} else {
		layout := ""
		for _, l := range naiveLayouts {
			if _, err := time.Parse(l, s); err == nil {
				layout = l
				break
			}
		}
		if layout == "" {
			return time.Time{}, fmt.Errorf("at=%q is not a time. Accepted: %s.", s, atForms)
		}
		inSystem, _ := time.ParseInLocation(layout, s, system)
		if profile == nil {
			return time.Time{}, fmt.Errorf("at=%q has no offset, and your Slack profile zone could not be read to check it against this machine's zone (%s). Give an offset, e.g. '%s'.",
				s, zoneName(system, inSystem), inSystem.Format(time.RFC3339))
		}
		inProfile, _ := time.ParseInLocation(layout, s, profile)
		if !inProfile.Equal(inSystem) {
			return time.Time{}, fmt.Errorf("at=%q has no offset, and your Slack profile zone (%s, %s) and this machine's zone (%s, %s) disagree at that time. Give an offset: '%s' or '%s'.",
				s, zoneName(profile, inProfile), offsetText(inProfile), zoneName(system, inSystem), offsetText(inSystem),
				inProfile.Format(time.RFC3339), inSystem.Format(time.RFC3339))
		}
		// A wall-clock time a DST change skips or repeats has no single
		// instant; Go would pick one silently.
		wall, _ := time.Parse(layout, s)
		repeats := inProfile.Add(-time.Hour).Format(layout) == wall.Format(layout) || inProfile.Add(time.Hour).In(profile).Format(layout) == wall.Format(layout)
		if inProfile.Format(layout) != wall.Format(layout) || repeats {
			return time.Time{}, fmt.Errorf("at=%q falls in a daylight-saving change in %s, so that wall-clock time does not exist or occurs twice. Give an offset.", s, zoneName(profile, inProfile))
		}
		at = inProfile
	}
	if lead := at.Sub(now); lead < scheduleMinLead {
		return time.Time{}, fmt.Errorf("at= resolves to %s, %s; a scheduled send must be at least 2 minutes out (Slack refuses closer times).",
			at.UTC().Format("2006-01-02 15:04:05 UTC"), relative(at, now))
	} else if lead > scheduleMaxLead {
		return time.Time{}, fmt.Errorf("at= resolves to %s, %s; Slack schedules at most 120 days out.",
			at.UTC().Format("2006-01-02 15:04 UTC"), relative(at, now))
	}
	return at, nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// zoneName is a location's IANA name, or its abbreviation at t for Local.
func zoneName(loc *time.Location, t time.Time) string {
	if name := loc.String(); name != "Local" && name != "" {
		return name
	}
	abbr, _ := t.Zone()
	return abbr
}

func offsetText(t time.Time) string {
	return "UTC" + t.Format("-07:00")
}

// relative is the distance from now: "in 14h 12m", "in 3d 2h", "2m ago".
func relative(at, now time.Time) string {
	d := at.Sub(now)
	ago := d < 0
	if ago {
		d = -d
	}
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	hours := int(d % (24 * time.Hour) / time.Hour)
	mins := int(d % time.Hour / time.Minute)
	var s string
	switch {
	case days > 0:
		s = fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		s = fmt.Sprintf("%dh %dm", hours, mins)
	default:
		s = fmt.Sprintf("%dm", mins)
	}
	if ago {
		return s + " ago"
	}
	return "in " + s
}

// zones are where a scheduled time renders: the profile zone (the system
// zone when the profile's is unknown), and the system zone beside it when
// its offset differs at that time.
type zones struct {
	profile *time.Location
	system  *time.Location
}

// render is a scheduled time as ADR-016 states it: absolute in the profile
// zone with abbreviation and offset, the system zone when it differs, UTC,
// and the distance from now.
func (z zones) render(at, now time.Time) string {
	primary := z.profile
	if primary == nil {
		primary = z.system
	}
	p := at.In(primary)
	s := fmt.Sprintf("%s %s (%s)", p.Format("Mon 2006-01-02 15:04"), zoneAbbr(p), offsetText(p))
	if z.profile != nil {
		if sys := at.In(z.system); sys.Format("-0700") != p.Format("-0700") {
			s += fmt.Sprintf(", %s %s on this machine", sys.Format("15:04"), zoneAbbr(sys))
		}
	}
	if p.Format("-0700") != "+0000" {
		s += ", " + at.UTC().Format("15:04") + " UTC"
	}
	return s + " — " + relative(at, now)
}

func zoneAbbr(t time.Time) string {
	abbr, _ := t.Zone()
	return abbr
}

// profileZone is the person's Slack profile zone, or nil when it cannot be
// read. A cached self user answers without a call.
func profileZone(ctx context.Context, ap *provider.ApiProvider) *time.Location {
	self := ap.SelfUserID()
	if self == "" {
		return nil
	}
	u, err := ap.ResolveUser(ctx, self)
	if err != nil || u.TZ == "" {
		return nil
	}
	loc, err := time.LoadLocation(u.TZ)
	if err != nil {
		log.Printf("schedule: profile zone %q not loadable: %v", u.TZ, err)
		return nil
	}
	return loc
}

// systemZone is the server's local zone; a variable so tests pin it.
var systemZone = func() *time.Location { return time.Local }

// scheduleNow is the clock the bounds and distances read; tests pin it.
var scheduleNow = time.Now
