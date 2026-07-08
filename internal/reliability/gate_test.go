package reliability

import (
	"errors"
	"testing"
)

// TestStateStringExhaustive guards the closed-set invariant: every declared
// State must have a non-fallback String(), so adding a failure mode without
// wiring it everywhere fails loudly here.
func TestStateStringExhaustive(t *testing.T) {
	for s := StateInvalid; s <= StateSchemaDrift; s++ {
		got := s.String()
		if got == "" {
			t.Fatalf("State(%d) has empty String()", int(s))
		}
		if len(got) > 6 && got[:6] == "State(" {
			t.Fatalf("State(%d) fell through to the numeric fallback: %q", int(s), got)
		}
	}
}

func TestTerminalOnlyOKContinues(t *testing.T) {
	if StateOK.Terminal() {
		t.Fatal("StateOK must be non-terminal")
	}
	for _, s := range []State{StateAuthCheckpoint, StateRateLimited, StateSchemaDrift} {
		if !s.Terminal() {
			t.Fatalf("%s must be terminal", s)
		}
	}
}

func TestFailRejectsOK(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Fail(StateOK, ...) must panic")
		}
	}()
	_ = Fail(StateOK, "nope")
}

func TestFloorEmptyKnownHistoryEscalatesToDrift(t *testing.T) {
	f := Floor{HasKnownHistory: true, ConsecutiveEmpty: 2, ConsecutiveEmptyLimit: 3}
	got := f.Apply(OK(0))
	if got.State != StateSchemaDrift {
		t.Fatalf("expected drift on 3rd empty run for known-history resource, got %s", got.State)
	}
}

func TestFloorEmptyUnknownHistoryStaysOK(t *testing.T) {
	f := Floor{HasKnownHistory: false, ConsecutiveEmptyLimit: 3}
	if got := f.Apply(OK(0)); got.State != StateOK {
		t.Fatalf("empty resource with no known history must stay OK, got %s", got.State)
	}
}

func TestFloorNonEmptyStaysOK(t *testing.T) {
	f := Floor{HasKnownHistory: true, ConsecutiveEmpty: 9, ConsecutiveEmptyLimit: 1}
	if got := f.Apply(OK(5)); got.State != StateOK || got.N != 5 {
		t.Fatalf("non-empty response must stay OK(5), got %s N=%d", got.State, got.N)
	}
}

func TestFloorNeverRescuesTerminal(t *testing.T) {
	f := Floor{HasKnownHistory: true, ConsecutiveEmptyLimit: 1}
	v := Fail(StateRateLimited, "429")
	if got := f.Apply(v); got.State != StateRateLimited {
		t.Fatalf("floor must not rescue a terminal verdict, got %s", got.State)
	}
}

func TestGateErrorIsNotDrift(t *testing.T) {
	// A gate-internal failure must be distinguishable from a remote contract
	// violation: it is an error value, not a StateSchemaDrift verdict.
	ge := &GateError{Op: "parse", Err: errors.New("boom")}
	var target *GateError
	if !errors.As(ge, &target) {
		t.Fatal("GateError must be unwrappable via errors.As")
	}
	if !errors.Is(ge, ge.Err) {
		t.Fatal("GateError must unwrap to its cause")
	}
}

func TestPageGuardStopsAtCap(t *testing.T) {
	g := NewPageGuard(2)
	if ok, _ := g.Next("a"); !ok {
		t.Fatal("page 1 should proceed")
	}
	if ok, _ := g.Next("b"); !ok {
		t.Fatal("page 2 should proceed")
	}
	ok, err := g.Next("c")
	if ok || err == nil {
		t.Fatal("page 3 must be refused at cap with an error")
	}
}

func TestPageGuardStopsOnNonAdvancingCursor(t *testing.T) {
	g := NewPageGuard(10)
	if ok, _ := g.Next("same"); !ok {
		t.Fatal("first fetch should proceed")
	}
	ok, err := g.Next("same")
	if ok || err == nil {
		t.Fatal("repeated cursor must be refused with an error")
	}
}

func TestPageGuardEmptyCursorIsNaturalEnd(t *testing.T) {
	g := NewPageGuard(10)
	ok, err := g.Next("")
	if ok {
		t.Fatal("empty cursor must stop the loop")
	}
	if err != nil {
		t.Fatalf("empty cursor is a natural end, not an anomaly: %v", err)
	}
}

func TestPageGuardRejectsMisconfiguration(t *testing.T) {
	g := NewPageGuard(0)
	if ok, err := g.Next("a"); ok || err == nil {
		t.Fatal("MaxPages<=0 must refuse to proceed")
	}
}
