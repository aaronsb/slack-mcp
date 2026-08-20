package estate_test

import (
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/estate"
)

func enc(day string) estate.Encounter {
	return estate.Encounter{User: "U1", Conv: "C1", Day: day}
}

func TestStripsFoldConsecutiveDaysIntoOneRun(t *testing.T) {
	s := estate.Strips([]estate.Encounter{enc("2026-08-01"), enc("2026-08-02"), enc("2026-08-03")}, 1)
	if len(s) != 1 || s[0].From != "2026-08-01" || s[0].To != "2026-08-03" || s[0].Days != 3 {
		t.Fatalf("strips = %+v", s)
	}
}

func TestStripsTolerateAOneDayGap(t *testing.T) {
	s := estate.Strips([]estate.Encounter{enc("2026-08-01"), enc("2026-08-03")}, 1)
	if len(s) != 1 || s[0].Days != 2 {
		t.Fatalf("one-day gap split the strip: %+v", s)
	}
}

func TestStripsSplitBeyondTheGapTolerance(t *testing.T) {
	s := estate.Strips([]estate.Encounter{enc("2026-08-01"), enc("2026-08-05")}, 1)
	if len(s) != 2 {
		t.Fatalf("three-day gap did not split: %+v", s)
	}
	if s[0].To != "2026-08-01" || s[1].From != "2026-08-05" {
		t.Fatalf("wrong strip bounds: %+v", s)
	}
}

func TestStripsCollapseHourBucketsToDays(t *testing.T) {
	h9, h10 := 9, 10
	s := estate.Strips([]estate.Encounter{
		{User: "U1", Conv: "C1", Day: "2026-08-01", Hour: &h9},
		{User: "U1", Conv: "C1", Day: "2026-08-01", Hour: &h10},
	}, 1)
	if len(s) != 1 || s[0].Days != 1 {
		t.Fatalf("hour buckets counted as extra days: %+v", s)
	}
}
