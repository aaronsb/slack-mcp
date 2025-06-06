package features

import (
	"fmt"
	"time"
)

// parseSlackTimestamp converts Slack timestamp to time.Time
func parseSlackTimestamp(ts string) time.Time {
	// Slack timestamps are Unix time with microseconds
	var sec, nsec int64
	fmt.Sscanf(ts, "%d.%d", &sec, &nsec)
	return time.Unix(sec, nsec*1000)
}

// formatTimestamp formats a time to human readable format
func formatTimestamp(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	if diff < time.Hour {
		minutes := int(diff.Minutes())
		if minutes < 1 {
			return "just now"
		}
		return fmt.Sprintf("%d minutes ago", minutes)
	} else if diff < 24*time.Hour {
		hours := int(diff.Hours())
		return fmt.Sprintf("%d hours ago", hours)
	} else if diff < 48*time.Hour {
		return fmt.Sprintf("Yesterday at %s", t.Format("3:04 PM"))
	} else if diff < 7*24*time.Hour {
		return t.Format("Monday at 3:04 PM")
	}
	return t.Format("Jan 2 at 3:04 PM")
}
