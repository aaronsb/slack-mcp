package text

import "testing"

func TestChannelLabel(t *testing.T) {
	cases := []struct {
		name  string
		mpim  bool
		label string
	}{
		{"eng", false, "#eng"},
		{"mpdm-alice--bob--carol-1", true, "group: alice, bob, carol"},
		{"mpdm-alice--bob-12", true, "group: alice, bob"},
		{"", true, "group DM"},
	}
	for _, c := range cases {
		if got := ChannelLabel(c.name, c.mpim); got != c.label {
			t.Errorf("ChannelLabel(%q, %v) = %q, want %q", c.name, c.mpim, got, c.label)
		}
	}
}

func TestGroupDMNameUsesDisplayNames(t *testing.T) {
	got := GroupDMName("mpdm-alice--bob-1", map[string]string{"alice": "Alice Martin"})
	if got != "group: Alice Martin, bob" {
		t.Errorf("GroupDMName = %q, want display name for alice and handle for bob", got)
	}
}
