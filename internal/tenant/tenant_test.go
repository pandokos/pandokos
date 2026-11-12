package tenant

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestStates is T-101.
func TestStates(t *testing.T) {
	want := []State{StateProvisioning, StateActive, StateSuspended, StateDeleting, StateDeleted}
	if got := States(); !reflect.DeepEqual(got, want) {
		t.Fatalf("States() = %v, want %v", got, want)
	}
	for i, name := range []string{"provisioning", "active", "suspended", "deleting", "deleted"} {
		s, ok := ParseState(name)
		if !ok || s != want[i] {
			t.Errorf("ParseState(%q) = %q, %v", name, s, ok)
		}
	}
	for _, bad := range []string{"", "Active", "ACTIVE", "activ", "active ", "pending", "disabled"} {
		if s, ok := ParseState(bad); ok {
			t.Errorf("ParseState(%q) accepted: %q", bad, s)
		}
	}
}

// TestTransitions is T-102.
func TestTransitions(t *testing.T) {
	allowed := map[[2]State]bool{
		{StateProvisioning, StateActive}:   true,
		{StateProvisioning, StateDeleting}: true,
		{StateActive, StateSuspended}:      true,
		{StateSuspended, StateActive}:      true,
		{StateActive, StateDeleting}:       true,
		{StateSuspended, StateDeleting}:    true,
		{StateDeleting, StateDeleted}:      true,
	}
	n := 0
	for _, from := range States() {
		for _, to := range States() {
			n++
			if got, want := CanTransition(from, to), allowed[[2]State{from, to}]; got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
	if n != 25 {
		t.Fatalf("checked %d pairs, want 25", n)
	}
	if CanTransition("bogus", StateActive) || CanTransition(StateActive, "bogus") {
		t.Error("an unknown state must not transition")
	}
}

// TestID is T-103.
func TestID(t *testing.T) {
	const good = "0190b2c4-7d3e-7a1b-8c2d-0123456789ab"
	id, err := ParseID(good)
	if err != nil {
		t.Fatalf("ParseID(%q): %v", good, err)
	}
	if id.String() != good {
		t.Errorf("String() = %q, want %q", id.String(), good)
	}
	if id.IsZero() {
		t.Error("a parsed ID is not zero")
	}
	var zero ID
	if !zero.IsZero() {
		t.Error("the zero ID must be zero")
	}
	bad := []string{
		"",
		strings.ToUpper(good),
		"{" + good + "}",
		strings.ReplaceAll(good, "-", ""),
		good[:35],
		good + "0",
		"00000000-0000-0000-0000-000000000000",
		"0190b2c4-7d3e-7a1b-8c2d-0123456789ag",
		"0190b2c47-d3e-7a1b-8c2d-0123456789ab",
		"urn:uuid:" + good,
	}
	for _, s := range bad {
		if _, err := ParseID(s); !errors.Is(err, ErrInvalidID) {
			t.Errorf("ParseID(%q) error = %v, want ErrInvalidID", s, err)
		}
	}
}
