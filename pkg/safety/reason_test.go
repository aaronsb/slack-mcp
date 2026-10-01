package safety

import (
	"bytes"
	"os"
	"testing"
	"time"
)

// The fold keeps each key's latest block reason and the reasons behind the
// strike count, so the clearing page can say what failed without the file
// holding the value.
func TestQuarantineStateKeepsReasons(t *testing.T) {
	for _, v := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(v, t.TempDir())
	}
	w := openTest(t, Soft)
	at := time.Date(2026, 10, 1, 14, 12, 0, 0, time.UTC)
	rec := func(d Destination, class, loc string, file int) {
		t.Helper()
		if _, err := w.Quarantine.RecordBlock(Block{
			Time: at, Destination: d,
			Call:  CallRecord{Tool: "say", Destination: d.Name},
			Match: MatchRecord{Class: class, Location: loc, File: file},
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec(channel("C1", "#a"), "jwt", "text", 0)                      // soft: warned, not quarantined
	rec(dm("D1", "U1", "@dana"), "aws-access-key", "file-bytes", 2) // quarantines @dana

	st := w.Quarantine.State()
	if len(st.StrikeReasons) != 2 || st.StrikeReasons[0].Class != "jwt" || st.StrikeReasons[1].Destination != "@dana" {
		t.Fatalf("strike reasons: %+v", st.StrikeReasons)
	}
	r, ok := st.ReasonFor(Person("U1", "@dana"))
	if !ok || r.Class != "aws-access-key" || r.Location != "file-bytes" || r.File != 2 || !r.Time.Equal(at) {
		t.Fatalf("reason for @dana: %+v %v", r, ok)
	}
	if _, ok := st.ReasonFor(Conversation("C1", "#a")); ok {
		t.Fatal("an unquarantined destination has a reason")
	}

	if _, err := w.Quarantine.Clear(Person("U1", "@dana"), ByWeb, "", at); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Quarantine.Clear(StrikesKey, ByWeb, "", at); err != nil {
		t.Fatal(err)
	}
	st = w.Quarantine.State()
	if _, ok := st.ReasonFor(Person("U1", "@dana")); ok || len(st.StrikeReasons) != 0 {
		t.Fatalf("reasons survive their clears: %+v", st)
	}
	raw, _ := os.ReadFile(qpath(w))
	if bytes.Count(raw, []byte(`"by":"web"`)) != 2 {
		t.Fatalf("web clears not marked:\n%s", raw)
	}
}
