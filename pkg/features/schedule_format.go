package features

import (
	"fmt"
	"strings"
)

// Renderers for scheduled send (ADR-016). Every time is the absolute,
// zoned form zones.render built; the echo above carries at= as passed.

func formatSayScheduled(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return result.Message + footer(result)
	}
	var b strings.Builder
	where := str(data, "destination")
	if th := str(data, "thread"); th != "" {
		where = "a thread in " + where
		if bc, _ := data["broadcast"].(bool); bc {
			where += ", also to the channel"
		}
	}
	fmt.Fprintf(&b, "Scheduled for %s — to %s.\n", str(data, "when"), where)
	fmt.Fprintf(&b, "Slack sends it at that time (%s); nothing here needs to stay running.\n", str(data, "account"))
	fmt.Fprintf(&b, "> %s\n", str(data, "preview"))
	fmt.Fprintf(&b, "Handle: %s", str(data, "handle"))
	b.WriteString(footer(result))
	return b.String()
}

func formatMessagesScheduled(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return result.Message + footer(result)
	}
	items := asList(data["items"])
	var b strings.Builder
	scope := ""
	if n := str(data, "narrowedTo"); n != "" {
		scope = " to " + n
	}
	noun := "messages"
	if len(items) == 1 {
		noun = "message"
	}
	fmt.Fprintf(&b, "%d scheduled %s%s, soonest first. Your unsent composer drafts are never shown.\n", len(items), noun, scope)
	for _, it := range items {
		where := str(it, "destination")
		if th := str(it, "thread"); th != "" {
			where += " (thread " + th
			if bc, _ := it["broadcast"].(bool); bc {
				where += ", also to the channel"
			}
			where += ")"
		}
		fmt.Fprintf(&b, "- %s · %s · %q · %s · %s\n", str(it, "when"), where, str(it, "preview"), str(it, "origin"), str(it, "handle"))
	}
	if more, _ := data["hasMore"].(bool); more {
		b.WriteString("Slack's draft list was cut at its page and offers no next page, so more scheduled messages may exist beyond these.\n")
	}
	if len(items) > 0 {
		b.WriteString("**Next:** Cancel one: say cancel='<handle>'")
	}
	b.WriteString(footer(result))
	return strings.TrimRight(b.String(), "\n")
}

func formatSayCancelled(result *FeatureResult) string {
	data := dataMap(result)
	if data == nil {
		return result.Message + footer(result)
	}
	return fmt.Sprintf("Cancelled the message scheduled for %s to %s:\n> %s", str(data, "when"), str(data, "destination"), str(data, "preview")) + footer(result)
}
