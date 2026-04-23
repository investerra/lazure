package errs

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCode_Nil(t *testing.T) {
	if got := Code(nil); got != CodeOK {
		t.Errorf("Code(nil) = %d, want %d", got, CodeOK)
	}
}

func TestCode_UntaggedError(t *testing.T) {
	err := errors.New("plain")
	if got := Code(err); got != CodeTask {
		t.Errorf("Code(plain) = %d, want %d (default task failure)", got, CodeTask)
	}
}

func TestCode_TaggedVariants(t *testing.T) {
	cases := []struct {
		name string
		wrap func(error) error
		want int
	}{
		{"Validation", Validation, CodeTask},
		{"Drift", Drift, CodeTask},
		{"Auth", Auth, CodeSystem},
		{"Usage", Usage, CodeSystem},
		{"System", System, CodeSystem},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.wrap(errors.New("underlying"))
			if got := Code(err); got != tc.want {
				t.Errorf("Code(%s(err)) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

func TestCode_ThroughWrapChain(t *testing.T) {
	// Tagged error buried inside pkg/errors wrap layers must still
	// classify correctly via errors.As.
	base := Validation(errors.New("bad"))
	outer := Wrap(base, "loading")
	outer = Wrap(outer, "deploy")
	if got := Code(outer); got != CodeTask {
		t.Errorf("Code(nested) = %d, want %d", got, CodeTask)
	}
}

func TestCode_WithCodeNil(t *testing.T) {
	// Wrapping nil must stay nil (don't manufacture an exit code).
	if err := WithCode(CodeTask, nil); err != nil {
		t.Errorf("WithCode(1, nil) should be nil, got %v", err)
	}
}

func TestWrap_PreservesMessageChain(t *testing.T) {
	base := errors.New("root cause")
	wrapped := Wrap(base, "layer 1")
	wrapped = Wrap(wrapped, "layer 2")
	msg := wrapped.Error()
	for _, want := range []string{"root cause", "layer 1", "layer 2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestWrapf_FormatsMessage(t *testing.T) {
	err := Wrapf(errors.New("base"), "context %d-%s", 42, "x")
	if !strings.Contains(err.Error(), "context 42-x") {
		t.Errorf("Wrapf did not format: %q", err.Error())
	}
}

// TestFormat_PlusV verifies the tagged coded type delegates %+v to the
// underlying pkg/errors error so the stack trace survives classification.
func TestFormat_PlusV(t *testing.T) {
	err := Validation(Wrap(errors.New("root"), "ctx"))
	plusV := fmt.Sprintf("%+v", err)
	// pkg/errors %+v output contains the goroutine function name, which
	// always ends up with a period somewhere in the frame list.
	if !strings.Contains(plusV, "root") {
		t.Errorf("%%+v should contain root cause: %s", plusV)
	}
	// Should contain stack trace frame info. pkg/errors emits the test
	// function name in the stack trace.
	if !strings.Contains(plusV, "TestFormat_PlusV") {
		t.Errorf("%%+v should contain stack trace frames; got:\n%s", plusV)
	}
}

func TestFormat_V(t *testing.T) {
	// %v should just print the message chain, no stack.
	err := Validation(Wrap(errors.New("root"), "ctx"))
	v := fmt.Sprintf("%v", err)
	if strings.Contains(v, "TestFormat_V") {
		t.Errorf("%%v should NOT include stack frames; got:\n%s", v)
	}
	if !strings.Contains(v, "root") || !strings.Contains(v, "ctx") {
		t.Errorf("%%v should include message chain; got %q", v)
	}
}

// TestUnwrap_ErrorsIs verifies a tagged-then-wrapped chain still works
// with stdlib errors.Is for specific sentinel matching.
func TestUnwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("specific")
	err := Validation(Wrap(sentinel, "context"))
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is should find the root sentinel through a tagged+wrapped chain")
	}
}

func TestNew_StackAttached(t *testing.T) {
	err := New("fresh error")
	plusV := fmt.Sprintf("%+v", err)
	if !strings.Contains(plusV, "TestNew_StackAttached") {
		t.Errorf("New() should attach a stack; got:\n%s", plusV)
	}
}

func TestErrorf_StackAttached(t *testing.T) {
	err := Errorf("counted %d items", 5)
	if !strings.Contains(err.Error(), "counted 5 items") {
		t.Errorf("Errorf did not format: %q", err.Error())
	}
	plusV := fmt.Sprintf("%+v", err)
	if !strings.Contains(plusV, "TestErrorf_StackAttached") {
		t.Errorf("Errorf should attach a stack; got:\n%s", plusV)
	}
}

// ---------- Silent / IsSilent ----------

func TestSilent_CarriesCode(t *testing.T) {
	err := Silent(42, errors.New("child exited"))
	if got := Code(err); got != 42 {
		t.Errorf("Code = %d, want 42", got)
	}
	if !IsSilent(err) {
		t.Error("IsSilent should be true for Silent-tagged error")
	}
}

func TestSilent_NilStaysNil(t *testing.T) {
	if err := Silent(5, nil); err != nil {
		t.Errorf("Silent(5, nil) = %v, want nil", err)
	}
}

func TestSilent_ThroughWrapChain(t *testing.T) {
	// Even buried inside Wrap layers, IsSilent still detects the flag
	// via errors.As — so callers can freely add context above a Silent.
	base := Silent(7, errors.New("az failed"))
	outer := Wrap(base, "running exec")
	if !IsSilent(outer) {
		t.Error("IsSilent should walk the wrap chain")
	}
	if got := Code(outer); got != 7 {
		t.Errorf("Code = %d, want 7", got)
	}
}

func TestIsSilent_NonSilentTaggedError(t *testing.T) {
	// A regular Validation/System/Auth error must NOT be silent.
	for _, wrap := range []func(error) error{Validation, System, Auth, Usage, Drift} {
		err := wrap(errors.New("x"))
		if IsSilent(err) {
			t.Errorf("IsSilent should be false for non-Silent tag: %v", err)
		}
	}
}

func TestIsSilent_NilFalse(t *testing.T) {
	if IsSilent(nil) {
		t.Error("IsSilent(nil) should be false")
	}
}

func TestIsSilent_PlainErrorFalse(t *testing.T) {
	if IsSilent(errors.New("plain")) {
		t.Error("IsSilent(plain) should be false")
	}
}
