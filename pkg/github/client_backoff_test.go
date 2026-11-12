package github_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

func list(f *factory) error {
	_, err := f.client.ListIssues(bg, repoRef, github.ListIssuesOptions{})
	return err
}

func TestSecondaryRateLimitIsWaitedOutThenSucceeds(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.fake.LimitNext(2, 7*time.Second)
	if err := list(f); err != nil {
		t.Fatalf("a limit that clears within the retries must not fail: %v", err)
	}
	if got := f.clock.recorded(); !slices.Equal(got, []time.Duration{7 * time.Second, 7 * time.Second}) {
		t.Fatalf("waits = %v, want the Retry-After twice", got)
	}
	if n := len(f.fake.Requests()); n != 3 {
		t.Fatalf("%d requests, want 3", n)
	}
}

func TestPrimaryRateLimitWaitsUntilTheReset(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.fake.LimitPrimaryNext(1, f.clock.Now().Add(30*time.Second))
	if err := list(f); err != nil {
		t.Fatal(err)
	}
	if got := f.clock.recorded(); !slices.Equal(got, []time.Duration{30 * time.Second}) {
		t.Fatalf("waits = %v, want to wait until the reset", got)
	}
}

func TestLimitLongerThanTheWaitCeilingIsReportedNotSlept(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.fake.LimitNext(1, time.Hour)
	err := list(f)
	if !core.HasCode(err, core.CodeRateLimited) {
		t.Fatalf("err = %v, want RATE_LIMITED", err)
	}
	var typed *core.Error
	if !asCore(err, &typed) || typed.Details["retry_after_seconds"] != "3600" {
		t.Fatalf("the error must tell the caller when to come back: %+v", typed)
	}
	if len(f.clock.recorded()) != 0 || len(f.fake.Requests()) != 1 {
		t.Fatalf("waits %v, requests %d: a wait over the ceiling must return at once", f.clock.recorded(), len(f.fake.Requests()))
	}
}

// A limit whose wait would outlast the caller's deadline is reported as
// the rate limit it is, at once, not slept through until the deadline turns
// it into a timeout; a deadline the wait fits within is waited out.
func TestLimitOutlastingTheDeadlineIsReportedNotSlept(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.fake.LimitNext(1, time.Minute)
	ctx, cancel := context.WithTimeout(bg, 30*time.Second)
	defer cancel()
	_, err := f.client.ListIssues(ctx, repoRef, github.ListIssuesOptions{})
	if !core.HasCode(err, core.CodeRateLimited) {
		t.Fatalf("err = %v, want RATE_LIMITED", err)
	}
	var typed *core.Error
	if !asCore(err, &typed) || typed.Details["retry_after_seconds"] != "60" {
		t.Fatalf("the error must tell the caller when to come back: %+v", typed)
	}
	if len(f.clock.recorded()) != 0 || len(f.fake.Requests()) != 1 {
		t.Fatalf("waits %v, requests %d: a wait past the deadline must return at once", f.clock.recorded(), len(f.fake.Requests()))
	}
	f.fake.LimitNext(1, 7*time.Second)
	ctx, cancel = context.WithTimeout(bg, 10*time.Minute)
	defer cancel()
	if _, err := f.client.ListIssues(ctx, repoRef, github.ListIssuesOptions{}); err != nil {
		t.Fatalf("a limit that clears before the deadline must be waited out: %v", err)
	}
	if got := f.clock.recorded(); !slices.Equal(got, []time.Duration{7 * time.Second}) {
		t.Fatalf("waits = %v, want the Retry-After once", got)
	}
}

func TestPersistentRateLimitFailsAfterTheAttemptsAreUsed(t *testing.T) {
	t.Parallel()
	f := newFactory(t, func(c *github.ClientConfig) { c.MaxAttempts = 3 })
	f.fake.LimitNext(10, time.Second)
	if err := list(f); !core.HasCode(err, core.CodeRateLimited) {
		t.Fatalf("err = %v, want RATE_LIMITED, never success", err)
	}
	if n := len(f.fake.Requests()); n != 3 {
		t.Fatalf("%d requests, want exactly MaxAttempts", n)
	}
}

func TestPlainForbiddenIsAPermissionFailureAndIsNotRetried(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.fake.FailNext(1, http.StatusForbidden)
	if err := list(f); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("err = %v, want PERMISSION_DENIED", err)
	}
	if len(f.clock.recorded()) != 0 || len(f.fake.Requests()) != 1 {
		t.Fatal("a permission failure must not be retried")
	}
}

func TestReadsRetryServerErrorsWithGrowingBackoff(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.fake.FailNext(2, http.StatusServiceUnavailable)
	if err := list(f); err != nil {
		t.Fatal(err)
	}
	if got := f.clock.recorded(); !slices.Equal(got, []time.Duration{500 * time.Millisecond, time.Second}) {
		t.Fatalf("backoff = %v", got)
	}
}

func TestPersistentOutageIsUnavailableNotSuccess(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.fake.FailNext(10, http.StatusBadGateway)
	if err := list(f); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("err = %v, want UNAVAILABLE", err)
	}
}

func TestWritesThatMayHaveLandedAreNotRepeatedAfterAServerError(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	n := mustOpen(t, f, f.person("alice"), "idea")
	f.fake.FailNext(1, http.StatusBadGateway)
	_, err := f.client.CreateComment(bg, repoRef.Issue(n), "status")
	if !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("err = %v, want UNAVAILABLE", err)
	}
	if len(f.clock.recorded()) != 0 || len(f.fake.Requests()) != 1 {
		t.Fatal("a POST must not be retried after a 5xx: it could create a second comment")
	}
}

func TestRateLimitedWritesAreRetriedBecauseGitHubRefusedThem(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	n := mustOpen(t, f, f.person("alice"), "idea")
	f.fake.LimitNext(1, 2*time.Second)
	if _, err := f.client.CreateComment(bg, repoRef.Issue(n), "status"); err != nil {
		t.Fatal(err)
	}
	if comments, _ := f.fake.Comments(testRepo, n); len(comments) != 1 {
		t.Fatalf("%d comments after a retried rate-limited write, want exactly 1", len(comments))
	}
}

func TestCancelledContextEndsAWaitInsteadOfHanging(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.clock.block = true
	f.fake.LimitNext(1, time.Second)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	_, err := f.client.ListIssues(ctx, repoRef, github.ListIssuesOptions{})
	if err == nil || core.HasCode(err, core.CodeRateLimited) {
		t.Fatalf("err = %v, want a cancellation error", err)
	}
}
