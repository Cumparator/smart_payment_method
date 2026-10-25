package payment

import "testing"

func TestTransitions(t *testing.T) {
	cases := []struct {
		from, to State
		ok       bool
	}{
		{Received, Processing, true},
		{Processing, Succeeded, true},
		{Processing, Reconciling, true},
		{Processing, Received, true},
		{Reconciling, ManualReview, true},
		{Reconciling, Received, true},
		{Received, Succeeded, false},
		{Succeeded, Received, false},
		{Failed, Processing, false},
		{ManualReview, Received, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.ok {
			t.Errorf("CanTransition(%s, %s) = %v, want %v", c.from, c.to, got, c.ok)
		}
	}
}

func TestFinalStatesHaveNoTransitions(t *testing.T) {
	for _, s := range []State{Succeeded, Failed, ManualReview} {
		if !s.IsFinal() {
			t.Errorf("%s должен быть финальным", s)
		}
		if len(transitions[s]) != 0 {
			t.Errorf("из финального %s не должно быть переходов", s)
		}
	}
}
