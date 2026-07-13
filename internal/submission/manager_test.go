package submission_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prof18/pcenter-cli/internal/fakestore"
	"github.com/prof18/pcenter-cli/internal/store"
	"github.com/prof18/pcenter-cli/internal/submission"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Sleep(_ context.Context, duration time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, duration)
	c.now = c.now.Add(duration)
	return nil
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

type fixedRand float64

func (r fixedRand) Float64() float64 { return float64(r) }

func TestFinalizeRolloutHappyAnd504Succeeded(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		scenario  fakestore.MutationScenario
		wantSleep []time.Duration
	}{
		{name: "happy"},
		{name: "504 succeeded", scenario: fakestore.MutationScenario{Failures: 1, Status: 504, ApplyOnFailure: true}, wantSleep: []time.Duration{20 * time.Second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := rolloutServer(t, test.scenario)
			manager, clock := newManager(t, server)
			rollout, err := manager.FinalizeRollout(context.Background(), "APP", "published")
			if err != nil {
				t.Fatal(err)
			}
			if rollout.PackageRolloutStatus != "PackageRolloutCompleted" {
				t.Fatalf("rollout = %+v", rollout)
			}
			assertDurations(t, clock.Sleeps(), test.wantSleep)
		})
	}
}

func TestFinalizeRolloutGenuineFailureExhaustsVerifyLoop(t *testing.T) {
	t.Parallel()
	server := rolloutServer(t, fakestore.MutationScenario{Failures: 5, Status: 504})
	manager, clock := newManager(t, server)
	_, err := manager.FinalizeRollout(context.Background(), "APP", "published")
	if err == nil {
		t.Fatal("persistent finalize failure unexpectedly succeeded")
	}
	assertDurations(t, clock.Sleeps(), []time.Duration{20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 240 * time.Second})
}

func TestCreateSubmissionHappyAdoptedAndGenuineFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		scenario  fakestore.MutationScenario
		wantError bool
		wantSleep []time.Duration
	}{
		{name: "happy"},
		{name: "504 adopted", scenario: fakestore.MutationScenario{Failures: 1, Status: 504, ApplyOnFailure: true}, wantSleep: []time.Duration{20 * time.Second}},
		{name: "genuine failure", scenario: fakestore.MutationScenario{Failures: 4, Status: 504}, wantError: true, wantSleep: []time.Duration{20 * time.Second, 40 * time.Second, 80 * time.Second, 120 * time.Second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := fakestore.New(t, fakestore.Options{
				AppID: "APP", App: fakestore.App{ID: "APP"}, CreateSubmissionID: "created", CreateScenario: test.scenario,
			})
			manager, clock := newManager(t, server)
			created, err := manager.Create(context.Background(), "APP")
			if (err != nil) != test.wantError {
				t.Fatalf("created = %+v, error = %v", created, err)
			}
			if !test.wantError && created.ID != "created" {
				t.Fatalf("created = %+v", created)
			}
			assertDurations(t, clock.Sleeps(), test.wantSleep)
		})
	}
}

func TestDeleteDraftHappy504SucceededAndGenuineFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		scenario  fakestore.MutationScenario
		wantError bool
		wantSleep []time.Duration
	}{
		{name: "happy"},
		{name: "504 succeeded", scenario: fakestore.MutationScenario{Failures: 1, Status: 504, ApplyOnFailure: true}},
		{name: "genuine failure", scenario: fakestore.MutationScenario{Failures: 3, Status: 504}, wantError: true, wantSleep: []time.Duration{10 * time.Second, 20 * time.Second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := draftServer(t, test.scenario, fakestore.MutationScenario{}, nil, nil)
			manager, clock := newManager(t, server)
			err := manager.DeleteDraft(context.Background(), "APP", "draft")
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v", err)
			}
			assertDurations(t, clock.Sleeps(), test.wantSleep)
		})
	}
}

func TestCommitHappy504SucceededGenuineFailureAndPollTimeout(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		scenario   fakestore.MutationScenario
		statuses   []string
		wantError  bool
		wantWarn   bool
		wantSleeps []time.Duration
	}{
		{name: "happy", statuses: []string{"PreProcessing"}},
		{name: "504 succeeded", scenario: fakestore.MutationScenario{Failures: 1, Status: 504, ApplyOnFailure: true}, statuses: []string{"CommitStarted", "PreProcessing"}, wantSleeps: []time.Duration{15 * time.Second}},
		{name: "genuine failure", scenario: fakestore.MutationScenario{Failures: 4, Status: 504}, wantError: true, wantSleeps: []time.Duration{15 * time.Second, 30 * time.Second, 60 * time.Second, 120 * time.Second}},
		{name: "poll timeout warns", statuses: []string{"CommitStarted"}, wantWarn: true, wantSleeps: []time.Duration{2 * time.Second, 2 * time.Second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := draftServer(t, fakestore.MutationScenario{}, test.scenario, test.statuses, nil)
			manager, clock := newManager(t, server)
			result, err := manager.Commit(context.Background(), "APP", "draft", submission.PollOptions{Interval: 2 * time.Second, Attempts: 3})
			if (err != nil) != test.wantError {
				t.Fatalf("result = %+v error = %v", result, err)
			}
			if result.Warning != "" != test.wantWarn {
				t.Fatalf("warning = %q, want warning %t", result.Warning, test.wantWarn)
			}
			assertDurations(t, clock.Sleeps(), test.wantSleeps)
		})
	}
}

func TestCommitFailedIncludesStatusDetails(t *testing.T) {
	t.Parallel()
	details := json.RawMessage(`{"errors":["bad package"]}`)
	server := draftServer(t, fakestore.MutationScenario{}, fakestore.MutationScenario{}, []string{"CommitFailed"}, details)
	manager, _ := newManager(t, server)
	_, err := manager.Commit(context.Background(), "APP", "draft", submission.PollOptions{Interval: time.Second, Attempts: 1})
	if err == nil || !strings.Contains(err.Error(), "bad package") {
		t.Fatalf("error = %v", err)
	}
}

func TestFinalizeHandles401Refresh429RetryAfterAnd409Permanently(t *testing.T) {
	t.Parallel()
	t.Run("401 refresh", func(t *testing.T) {
		t.Parallel()
		server := rolloutServer(t, fakestore.MutationScenario{})
		server.SetFailures([]fakestore.Failure{{Method: http.MethodPost, Path: "/v1.0/my/applications/APP/submissions/published/finalizepackagerollout", Status: 401, Count: 1}})
		manager, clock := newManager(t, server)
		if _, err := manager.FinalizeRollout(context.Background(), "APP", "published"); err != nil {
			t.Fatal(err)
		}
		assertDurations(t, clock.Sleeps(), nil)
	})

	t.Run("429 retry after in verification GET", func(t *testing.T) {
		t.Parallel()
		server := rolloutServer(t, fakestore.MutationScenario{Failures: 1, Status: 504, ApplyOnFailure: true})
		server.SetFailures([]fakestore.Failure{{Method: http.MethodGet, Path: "/v1.0/my/applications/APP/submissions/published/packagerollout", Status: 429, RetryAfter: "17", Count: 1}})
		manager, clock := newManager(t, server)
		if _, err := manager.FinalizeRollout(context.Background(), "APP", "published"); err != nil {
			t.Fatal(err)
		}
		assertDurations(t, clock.Sleeps(), []time.Duration{20 * time.Second, 17 * time.Second})
	})

	t.Run("429 retry after on operation", func(t *testing.T) {
		t.Parallel()
		server := rolloutServer(t, fakestore.MutationScenario{})
		server.SetFailures([]fakestore.Failure{{Method: http.MethodPost, Path: "/v1.0/my/applications/APP/submissions/published/finalizepackagerollout", Status: 429, RetryAfter: "17", Count: 1}})
		manager, clock := newManager(t, server)
		if _, err := manager.FinalizeRollout(context.Background(), "APP", "published"); err != nil {
			t.Fatal(err)
		}
		assertDurations(t, clock.Sleeps(), []time.Duration{17 * time.Second})
	})

	t.Run("409 is permanent with actual state", func(t *testing.T) {
		t.Parallel()
		server := rolloutServer(t, fakestore.MutationScenario{})
		server.SetFailures([]fakestore.Failure{{Method: http.MethodPost, Path: "/v1.0/my/applications/APP/submissions/published/finalizepackagerollout", Status: 409, Count: 1}})
		manager, clock := newManager(t, server)
		_, err := manager.FinalizeRollout(context.Background(), "APP", "published")
		if err == nil || !strings.Contains(err.Error(), "PackageRolloutInProgress") {
			t.Fatalf("error = %v", err)
		}
		assertDurations(t, clock.Sleeps(), nil)
	})
}

func TestEveryRolloutMutationMaps409ToActualStateWithoutRetry(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		suffix string
		run    func(context.Context, *submission.Manager) error
	}{
		{name: "finalize", suffix: "/finalizepackagerollout", run: func(ctx context.Context, manager *submission.Manager) error {
			_, err := manager.FinalizeRollout(ctx, "APP", "published")
			return err
		}},
		{name: "set percentage", suffix: "/updatepackagerolloutpercentage", run: func(ctx context.Context, manager *submission.Manager) error {
			_, err := manager.SetRolloutPercentage(ctx, "APP", "published", 50)
			return err
		}},
		{name: "halt", suffix: "/haltpackagerollout", run: func(ctx context.Context, manager *submission.Manager) error {
			_, err := manager.HaltRollout(ctx, "APP", "published")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := rolloutServer(t, fakestore.MutationScenario{})
			path := "/v1.0/my/applications/APP/submissions/published" + test.suffix
			server.SetFailures([]fakestore.Failure{{Method: http.MethodPost, Path: path, Status: 409, Count: 1}})
			manager, clock := newManager(t, server)
			err := test.run(context.Background(), manager)
			if err == nil || !strings.Contains(err.Error(), "PackageRolloutInProgress") {
				t.Fatalf("error = %v", err)
			}
			assertDurations(t, clock.Sleeps(), nil)
			if countJournalRequests(server.Journal(), http.MethodPost, test.suffix) != 1 {
				t.Fatalf("operation retried: %+v", server.Journal())
			}
		})
	}
}

func TestStatusClassificationCoversFullTaxonomy(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"CommitFailed", "PreProcessingFailed", "CertificationFailed", "PublishFailed", "ReleaseFailed"} {
		if got := submission.ClassifyStatus(status); got != submission.StatusFailed {
			t.Fatalf("%s classified as %s", status, got)
		}
	}
	if submission.ClassifyStatus("Published") != submission.StatusSuccess || submission.ClassifyStatus("Canceled") != submission.StatusNeutral {
		t.Fatal("terminal statuses misclassified")
	}
	for _, status := range []string{"None", "PendingCommit", "CommitStarted", "PendingPublication", "Publishing", "PreProcessing", "Certification", "Release"} {
		if got := submission.ClassifyStatus(status); got != submission.StatusInProgress {
			t.Fatalf("%s classified as %s", status, got)
		}
	}
}

func rolloutServer(t *testing.T, scenario fakestore.MutationScenario) *fakestore.Server {
	t.Helper()
	return fakestore.New(t, fakestore.Options{
		AppID:            "APP",
		App:              fakestore.App{ID: "APP", LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published", Status: "Published"}},
		Submissions:      map[string]json.RawMessage{"published": json.RawMessage(`{"id":"published","status":"Published"}`)},
		Rollouts:         map[string]fakestore.Rollout{"published": {IsPackageRollout: true, PackageRolloutPercentage: 90, PackageRolloutStatus: "PackageRolloutInProgress"}},
		FinalizeScenario: scenario,
	})
}

func draftServer(t *testing.T, deleteScenario, commitScenario fakestore.MutationScenario, statuses []string, details json.RawMessage) *fakestore.Server {
	t.Helper()
	return fakestore.New(t, fakestore.Options{
		AppID:          "APP",
		App:            fakestore.App{ID: "APP", PendingApplicationSubmission: &fakestore.SubmissionRef{ID: "draft", Status: "PendingCommit"}},
		Submissions:    map[string]json.RawMessage{"draft": json.RawMessage(`{"id":"draft","status":"PendingCommit"}`)},
		DeleteScenario: deleteScenario, CommitScenario: commitScenario, CommitStatuses: statuses, CommitStatusDetails: details,
	})
}

func newManager(t *testing.T, server *fakestore.Server) (*submission.Manager, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)}
	client, err := store.NewClient(store.ClientOptions{
		APIBase: server.APIBase(), LoginBase: server.LoginBase(), TenantID: "tenant", ClientID: "client", ClientSecret: "secret",
		HTTPClient: http.DefaultClient, Clock: clock, Rand: fixedRand(1), CorrelationID: "correlation",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := submission.NewManager(submission.ManagerOptions{Client: client, Clock: clock, Rand: fixedRand(1)})
	if err != nil {
		t.Fatal(err)
	}
	return manager, clock
}

func assertDurations(t *testing.T, got, want []time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("sleeps = %v, want %v", got, want)
		}
	}
}

func countJournalRequests(journal []fakestore.Request, method, suffix string) int {
	count := 0
	for _, request := range journal {
		if request.Method == method && strings.Contains(request.Path, suffix) {
			count++
		}
	}
	return count
}
