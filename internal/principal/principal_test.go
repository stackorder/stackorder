package principal

import (
	"errors"
	"strings"
	"testing"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/report"
)

func TestActor(t *testing.T) {
	cases := []struct {
		p    Principal
		want string
	}{
		{Principal{Kind: OIDC, Login: "octocat"}, "octocat"},
		{Principal{Kind: Session, Login: "octocat"}, "octocat"},
		{Principal{Kind: APIKey, Login: "ci"}, "apikey:ci"},
	}
	for _, c := range cases {
		if got := c.p.Actor(); got != c.want {
			t.Errorf("Actor(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestTypedErrorsMatchSentinels(t *testing.T) {
	locked := &LockedError{Conflicts: []v1.LockInfo{{StackKey: "stacks/prod/vpc", PRNumber: 12}, {StackKey: "stacks/x"}}}
	if !errors.Is(locked, ErrLocked) || errors.Is(locked, ErrRefused) {
		t.Fatal("LockedError should match only ErrLocked")
	}
	if msg := locked.Error(); !strings.Contains(msg, "stacks/prod/vpc (PR #12)") || !strings.Contains(msg, "stacks/x") {
		t.Fatal(msg)
	}

	refused := &RefusedError{Failures: []report.GateFailure{{Layer: 1, Name: "allowed_teams", Reason: "not a member of platform-eng"}}}
	if !errors.Is(refused, ErrRefused) {
		t.Fatal("RefusedError should match ErrRefused")
	}
	if msg := refused.Error(); !strings.Contains(msg, "layer 1 allowed_teams: not a member of platform-eng") {
		t.Fatal(msg)
	}

	invalid := &InvalidError{Field: "sha", Reason: "must be 40 hex characters"}
	if !errors.Is(invalid, ErrInvalid) {
		t.Fatal("InvalidError should match ErrInvalid")
	}
	if invalid.Error() != "invalid sha: must be 40 hex characters" {
		t.Fatal(invalid.Error())
	}
	if (&InvalidError{Reason: "bad body"}).Error() != "invalid: bad body" {
		t.Fatal("field-less message")
	}

	wrapped := Wrap(ErrNotFound, "run %s", "abc")
	if !errors.Is(wrapped, ErrNotFound) || wrapped.Error() != "not found: run abc" {
		t.Fatal(wrapped)
	}
}
