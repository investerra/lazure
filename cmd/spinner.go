package cmd

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// spinnerFrames is the classic 10-frame Braille spinner; 80 ms per
// frame reads as a smooth spin in most terminals.
var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

const spinnerTick = 80 * time.Millisecond

// waitSpinner prints a single-line spinner with an elapsed / remaining
// countdown to stderr. Callers update the descriptive message as status
// changes. Automatically no-ops when stderr is not a TTY (piped output,
// CI, etc.) so log files don't fill with ANSI cursor-movement noise.
type waitSpinner struct {
	mu       sync.Mutex
	message  string
	start    time.Time
	deadline time.Time

	stop chan struct{}
	done chan struct{}
	tty  bool
}

// newWaitSpinner prepares a spinner but does NOT start it. Call Start()
// when you're ready for animation to begin. Call Stop() to clear the
// line and return the cursor. The zero-value message is "working..." —
// callers typically update it immediately.
//
// Pass a zero time.Time for `deadline` to render only the elapsed time
// (no "X until timeout" suffix). Used for indefinite waits like ARM
// async-op polling where we don't know how long Azure will take.
func newWaitSpinner(deadline time.Time) *waitSpinner {
	return &waitSpinner{
		message:  "working...",
		start:    time.Now(),
		deadline: deadline,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		tty:      isStderrTerminal(),
	}
}

// Start begins the spinner animation in a background goroutine. Safe to
// call even when !tty — in that case Start is a no-op.
func (s *waitSpinner) Start() {
	if !s.tty {
		close(s.done)
		return
	}
	go s.run()
}

// SetMessage updates the descriptive text shown after the spinner
// glyph. Thread-safe.
func (s *waitSpinner) SetMessage(msg string) {
	s.mu.Lock()
	s.message = msg
	s.mu.Unlock()
}

// Stop halts the animation, clears the spinner line, and blocks until
// the goroutine has exited (so callers can print a final status
// message without it being overwritten).
func (s *waitSpinner) Stop() {
	select {
	case <-s.done:
		return // already stopped / never started
	default:
	}
	close(s.stop)
	<-s.done
}

func (s *waitSpinner) run() {
	defer close(s.done)
	ticker := time.NewTicker(spinnerTick)
	defer ticker.Stop()

	var i int
	for {
		select {
		case <-s.stop:
			// \r returns to column 0, \x1b[K clears to end of line.
			fmt.Fprint(os.Stderr, "\r\x1b[K")
			return
		case <-ticker.C:
			s.render(i)
			i++
		}
	}
}

func (s *waitSpinner) render(i int) {
	s.mu.Lock()
	msg := s.message
	s.mu.Unlock()

	elapsed := time.Since(s.start).Round(time.Second)

	// \r returns to column 0; \x1b[K clears line before re-printing so a
	// shorter new message doesn't leave trailing chars from the prior one.
	if s.deadline.IsZero() {
		// Open-ended wait: just elapsed, no countdown.
		fmt.Fprintf(os.Stderr, "\r\x1b[K%c %s (%s elapsed)",
			spinnerFrames[i%len(spinnerFrames)], msg, fmtDuration(elapsed))
		return
	}
	remaining := time.Until(s.deadline).Round(time.Second)
	if remaining < 0 {
		remaining = 0
	}
	fmt.Fprintf(os.Stderr, "\r\x1b[K%c %s (%s elapsed, %s until timeout)",
		spinnerFrames[i%len(spinnerFrames)], msg, fmtDuration(elapsed), fmtDuration(remaining))
}

// fmtDuration renders a duration as mm:ss for the countdown. time's
// default Duration.String gives "1m2.345s" which is noisier than we
// want in a single-line spinner.
func fmtDuration(d time.Duration) string {
	total := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

// isStderrTerminal reports whether os.Stderr is attached to a TTY.
// Spinners become cursor-movement garbage in pipes and log files, so
// we no-op when it's not a character device.
func isStderrTerminal() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
