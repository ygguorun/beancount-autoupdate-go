package git

import (
	"errors"
	"testing"
)

func TestIsNonFastForwardError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"non-fast-forward update: refs/heads/main", true},
		{"rejected: refs/heads/main (fetch first)", true},
		{"remote error: STALE INFO / internal server error", true},
		{"authentication required", false},
		{"failed to add files: packfile not found", false},
	}

	for _, c := range cases {
		if got := isNonFastForwardError(errors.New(c.msg)); got != c.want {
			t.Errorf("isNonFastForwardError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}

	if isNonFastForwardError(nil) {
		t.Error("isNonFastForwardError(nil) = true, want false")
	}
}
