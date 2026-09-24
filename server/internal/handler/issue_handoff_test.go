package handler

import "testing"

func TestHandoffDuplicateReasonIsStructured(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   bool
	}{
		{"already handed off this round", true},
		{"active run in progress", true},
		{"status has no routing behaviour", false},
		{"", false},
	} {
		if got := handoffDuplicateReason(tc.reason); got != tc.want {
			t.Errorf("handoffDuplicateReason(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}
