// Package errs unifies Lazure's error handling: pkg/errors for wrapping
// with context + stack traces, plus typed classification for CLI exit
// codes.
//
// Conventions:
//
//   - At layer boundaries (load, transform, api, cli), wrap underlying
//     errors with `errs.Wrap(err, "context")` so the message chain reads
//     top-down and `%+v` prints a stack trace.
//
//   - At known classification points, tag with a category constructor —
//     `errs.Validation(err)`, `errs.Auth(err)`, `errs.Usage(err)`,
//     `errs.Drift(err)`, `errs.System(err)` — so main.go's top-level
//     handler can map to exit codes.
//
// Exit-code mapping (see https://en.wikipedia.org/wiki/Exit_status):
//
//	0  success
//	1  task failure (validation, drift, known operational error)
//	2  usage / system / auth error (operator must fix before retry)
package errs

import (
	"errors"
	"fmt"

	pkgerrors "github.com/pkg/errors"
)

// Exit codes used throughout Lazure. Keep in sync with the table above.
const (
	CodeOK     = 0
	CodeTask   = 1
	CodeSystem = 2
)

// coded carries an exit code alongside a wrapped error. Format delegates
// to the wrapped error so %+v still surfaces pkg/errors stack traces.
type coded struct {
	code int
	err  error
}

func (c *coded) Error() string { return c.err.Error() }
func (c *coded) Unwrap() error { return c.err }
func (c *coded) Code() int     { return c.code }

func (c *coded) Format(s fmt.State, verb rune) {
	if f, ok := c.err.(fmt.Formatter); ok {
		f.Format(s, verb)
		return
	}
	fmt.Fprint(s, c.err.Error())
}

// coder is the minimal interface extracted by Code(). Any error type in
// the chain that satisfies it provides an exit-code hint.
type coder interface {
	Code() int
}

// WithCode tags err with a specific exit code.
func WithCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return &coded{code: code, err: err}
}

// Validation tags err as a manifest/vars/secrets validation failure
// (exit 1). Use at the boundary where a validate pass converts multiple
// findings into a single returnable error.
func Validation(err error) error { return WithCode(CodeTask, err) }

// Drift tags err as "diff detected a difference between rendered and
// deployed" (exit 1). Used by `lazure diff`.
func Drift(err error) error { return WithCode(CodeTask, err) }

// Auth tags err as an authentication / credential failure (exit 2). Use
// when azidentity or Azure Key Vault calls fail with a credential error.
func Auth(err error) error { return WithCode(CodeSystem, err) }

// Usage tags err as a CLI usage error — missing argument, bad flag
// value, file not found at the expected project-dir location (exit 2).
func Usage(err error) error { return WithCode(CodeSystem, err) }

// System tags err as a generic operator-fixable system error (exit 2):
// git not available, $EDITOR not set, invalid log-level, etc.
func System(err error) error { return WithCode(CodeSystem, err) }

// Code extracts the exit code for an error chain. Returns 0 for nil,
// the tagged code for a coded error (via errors.As, so it works through
// wrap chains), and CodeTask (1) for any other non-nil error.
func Code(err error) int {
	if err == nil {
		return CodeOK
	}
	var c coder
	if errors.As(err, &c) {
		return c.Code()
	}
	return CodeTask
}

// Wrap re-exports pkg/errors.Wrap so callers don't need a second import
// just for error wrapping. The wrapped error exposes a stack trace via
// %+v formatting.
func Wrap(err error, msg string) error {
	return pkgerrors.Wrap(err, msg)
}

// Wrapf is Wrap with fmt.Sprintf-style message formatting.
func Wrapf(err error, format string, args ...any) error {
	return pkgerrors.Wrapf(err, format, args...)
}

// New creates a new error with a stack trace attached.
func New(msg string) error {
	return pkgerrors.New(msg)
}

// Errorf creates a new error with a formatted message and stack trace.
func Errorf(format string, args ...any) error {
	return pkgerrors.Errorf(format, args...)
}
