package payment

import "testing"

func TestAttemptIsLive(t *testing.T) {
	live := map[AttemptState]bool{
		AttemptStarted:      true,
		AttemptUnknown:      true,
		AttemptApproved:     false,
		AttemptDeclined:     false,
		AttemptChannelError: false,
		AttemptCancelled:    false,
	}
	for s, want := range live {
		if got := s.IsLive(); got != want {
			t.Errorf("%s.IsLive() = %v, want %v", s, got, want)
		}
	}
}

func TestAttemptTransitions(t *testing.T) {
	cases := []struct {
		from, to AttemptState
		ok       bool
	}{
		{AttemptStarted, AttemptApproved, true},
		{AttemptStarted, AttemptChannelError, true},
		{AttemptStarted, AttemptUnknown, true},
		{AttemptUnknown, AttemptApproved, true},
		{AttemptUnknown, AttemptDeclined, true},
		{AttemptUnknown, AttemptCancelled, true},
		{AttemptUnknown, AttemptChannelError, false},
		{AttemptUnknown, AttemptStarted, false},
		{AttemptApproved, AttemptDeclined, false},
		{AttemptChannelError, AttemptStarted, false},
	}
	for _, c := range cases {
		err := ValidateAttemptTransition(c.from, c.to)
		if (err == nil) != c.ok {
			t.Errorf("ValidateAttemptTransition(%s, %s) = %v, ok want %v", c.from, c.to, err, c.ok)
		}
	}
}

func TestFinishedAttemptsHaveNoTransitions(t *testing.T) {
	for _, s := range []AttemptState{AttemptApproved, AttemptDeclined, AttemptChannelError, AttemptCancelled} {
		if len(attemptTransitions[s]) != 0 {
			t.Errorf("из завершённой попытки %s не должно быть переходов", s)
		}
	}
}
