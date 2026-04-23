package cmd

import (
	"strings"
	"testing"
	"time"
)

func TestFmtDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0:00"},
		{5 * time.Second, "0:05"},
		{59 * time.Second, "0:59"},
		{time.Minute, "1:00"},
		{90 * time.Second, "1:30"},
		{10 * time.Minute, "10:00"},
		{-3 * time.Second, "0:-3"}, // negative input — real call sites clamp before formatting
	}
	for _, tc := range cases {
		if got := fmtDuration(tc.in); got != tc.want {
			t.Errorf("fmtDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSpinnerMessage(t *testing.T) {
	cases := []struct {
		name      string
		status    restartStatus
		wantParts []string
	}{
		{
			name:      "initial — no replicas seen yet",
			status:    restartStatus{},
			wantParts: []string{"cycle replicas"},
		},
		{
			name:      "cycling — old replicas terminating",
			status:    restartStatus{baselineStillPresent: 2, newReady: 1, newTotal: 3},
			wantParts: []string{"2 old", "terminating", "1/3 new ready"},
		},
		{
			name:      "almost there — all new, some not ready",
			status:    restartStatus{newReady: 2, newTotal: 3},
			wantParts: []string{"2/3 new replicas ready"},
		},
		{
			name:      "done state (used transiently before Stop)",
			status:    restartStatus{newReady: 3, newTotal: 3},
			wantParts: []string{"3/3 new replicas ready"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := spinnerMessage(tc.status)
			for _, want := range tc.wantParts {
				if !strings.Contains(got, want) {
					t.Errorf("spinnerMessage(%+v) = %q, missing %q",
						tc.status, got, want)
				}
			}
		})
	}
}

// TestWaitSpinner_NoTTYNoops verifies the spinner behaves sanely when
// stderr isn't a terminal — Start/Stop must not block or error.
func TestWaitSpinner_NoTTYNoops(t *testing.T) {
	// In `go test`, os.Stderr is typically not a TTY, so tty=false and
	// the goroutine never starts. Start closes done immediately; Stop
	// returns without blocking.
	sp := newWaitSpinner(time.Now().Add(time.Minute))
	sp.Start()
	sp.SetMessage("irrelevant in non-tty")
	sp.Stop() // should return immediately

	// Double-Stop safe.
	sp.Stop()
}
