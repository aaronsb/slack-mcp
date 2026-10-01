package text

import "strings"

// ChannelLabel names a conversation for display: "#name" for a channel,
// and a group DM by its people, never by Slack's mpdm-… wire name.
func ChannelLabel(name string, isMpIM bool) string {
	if isMpIM {
		return GroupDMName(name, nil)
	}
	return "#" + name
}

// GroupDMName turns Slack's internal group-DM name into the people in it.
// The wire format is "mpdm-alice--bob--carol-1", which is not something to
// show anyone. byName maps a handle to a display name; a handle it lacks is
// shown as the handle.
func GroupDMName(raw string, byName map[string]string) string {
	trimmed := strings.TrimPrefix(raw, "mpdm-")
	if i := strings.LastIndex(trimmed, "-"); i > 0 {
		trimmed = trimmed[:i]
	}

	parts := strings.Split(trimmed, "--")
	if raw == "" || len(parts) == 0 {
		return "group DM"
	}

	people := make([]string, 0, len(parts))
	for _, p := range parts {
		if display, ok := byName[p]; ok {
			people = append(people, display)
		} else if p != "" {
			people = append(people, p)
		}
	}
	if len(people) == 0 {
		return "group DM"
	}
	return "group: " + strings.Join(people, ", ")
}
