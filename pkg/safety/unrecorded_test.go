package safety

import (
	"testing"
	"time"
)

func TestUnrecordedHoldLiftsOnlyByApprovedLift(t *testing.T) {
	ws, err := OpenDir(t.TempDir(), Org{TeamID: "T1", UserID: "U1"}, Strict)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if held, _ := ws.UnrecordedHeld(now); held {
		t.Fatalf("held before any failure")
	}
	ws.HoldUnrecorded("", now)
	if held, _ := ws.UnrecordedHeld(now); !held {
		t.Fatalf("a hold with no lift request must stay")
	}

	r, _, err := ws.Pending.Create(Request{Cases: []Case{CaseLift}, Tool: "say", Lift: []Key{StrikesKey}}, now)
	if err != nil {
		t.Fatal(err)
	}
	ws.RetryUnrecordedLift(r.ID)
	if held, id := ws.UnrecordedHeld(now); !held || id != r.ID {
		t.Fatalf("held=%v id=%q", held, id)
	}
	if _, err := ws.Pending.Deny(r, AnswerCLI, now); err != nil {
		t.Fatal(err)
	}
	if held, _ := ws.UnrecordedHeld(now); !held {
		t.Fatalf("a denied lift released the hold")
	}

	r2, _, err := ws.Pending.Create(Request{Cases: []Case{CaseLift}, Tool: "say", Lift: []Key{StrikesKey}}, now)
	if err != nil {
		t.Fatal(err)
	}
	ws.RetryUnrecordedLift(r2.ID)
	if _, err := ws.Approve(r2, now); err != nil {
		t.Fatal(err)
	}
	if held, _ := ws.UnrecordedHeld(now); held {
		t.Fatalf("an approved lift did not release the hold")
	}
}

// A clear of strikes written after the hold engaged releases it, from any
// process and even with no strike recorded; one written before does not.
func TestUnrecordedHoldLiftsOnStrikesClearAfterIt(t *testing.T) {
	dir := t.TempDir()
	org := Org{TeamID: "T1", UserID: "U1"}
	ws, err := OpenDir(dir, org, Strict)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := OpenDir(dir, org, Strict)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if ok, err := cli.Quarantine.Clear(StrikesKey, ByCLI, "", t0); err != nil || ok {
		t.Fatalf("clear with nothing recorded: %v %v", ok, err)
	}
	ws.HoldUnrecorded("", t0.Add(time.Second))
	if held, _ := ws.UnrecordedHeld(t0); !held {
		t.Fatalf("a clear from before the hold released it")
	}
	if _, err := cli.Quarantine.Clear(StrikesKey, ByCLI, "", t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if held, _ := ws.UnrecordedHeld(t0); held {
		t.Fatalf("a clear of strikes after the hold did not release it")
	}
}
