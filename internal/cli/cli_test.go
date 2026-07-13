package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prof18/pcenter-cli/internal/cli"
	"github.com/prof18/pcenter-cli/internal/config"
	"github.com/prof18/pcenter-cli/internal/fakestore"
	"github.com/prof18/pcenter-cli/internal/output"
)

func TestVersionDoesNotRequireCredentials(t *testing.T) {
	t.Parallel()
	stdout, stderr, exitCode := execute(t, nil, []string{"--output", "json", "version"}, cli.BuildInfo{Version: "v1.2.3", Commit: "abc", Date: "today"})
	if exitCode != output.ExitSuccess || stderr != "" {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	for _, expected := range []string{"v1.2.3", "abc", "today"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("version output missing %q: %s", expected, stdout)
		}
	}
}

func TestReadOnlyCommandsAgainstFakeStore(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	environment := fakeEnvironment(server)

	for _, test := range []struct {
		args     []string
		contains []string
	}{
		{args: []string{"app", "info"}, contains: []string{"Example", "published", "pending"}},
		{args: []string{"auth", "status"}, contains: []string{"Example", "Published", "PendingCommit"}},
		{args: []string{"locales", "list"}, contains: []string{"en-us", "it"}},
		{args: []string{"submission", "status"}, contains: []string{"published", "pending", "warning"}},
		{args: []string{"rollout", "status"}, contains: []string{"PackageRolloutInProgress", "fallback", "90"}},
	} {
		stdout, stderr, exitCode := execute(t, environment, append([]string{"--output", "json"}, test.args...), cli.BuildInfo{})
		if exitCode != output.ExitSuccess || stderr != "" {
			t.Fatalf("%v exit = %d stderr = %q", test.args, exitCode, stderr)
		}
		if !json.Valid([]byte(stdout)) {
			t.Fatalf("%v output is not JSON: %s", test.args, stdout)
		}
		for _, expected := range test.contains {
			if !strings.Contains(stdout, expected) {
				t.Fatalf("%v output missing %q: %s", test.args, expected, stdout)
			}
		}
	}
}

func TestSubmissionGetRedactsUploadURLUnlessOptedIn(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	environment := fakeEnvironment(server)

	stdout, stderr, exitCode := execute(t, environment, []string{"--output", "json", "submission", "get"}, cli.BuildInfo{})
	if exitCode != 0 || stderr != "" || strings.Contains(stdout, "sas-secret") || !strings.Contains(stdout, "?[REDACTED]") {
		t.Fatalf("default get stdout = %q stderr = %q exit = %d", stdout, stderr, exitCode)
	}
	stdout, stderr, exitCode = execute(t, environment, []string{"--output", "json", "submission", "get", "--include-upload-url"}, cli.BuildInfo{})
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "sas-secret") {
		t.Fatalf("opted-in get stdout = %q stderr = %q exit = %d", stdout, stderr, exitCode)
	}
}

func TestReviewsAllUsesWideDatesPagingAndComposedFilter(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	environment := fakeEnvironment(server)
	stdout, stderr, exitCode := execute(t, environment, []string{
		"--output", "json", "reviews", "list", "--all", "--market", "US", "--filter", "rating ge 4",
	}, cli.BuildInfo{})
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	for _, reviewID := range []string{"review-one", "review-two"} {
		if !strings.Contains(stdout, reviewID) {
			t.Fatalf("reviews output missing %s: %s", reviewID, stdout)
		}
	}

	journal := server.Journal()
	var reviewRequests []fakestore.Request
	for _, request := range journal {
		if request.Path == "/v1.0/my/analytics/reviews" {
			reviewRequests = append(reviewRequests, request)
		}
	}
	if len(reviewRequests) != 2 {
		t.Fatalf("review requests = %d, want 2", len(reviewRequests))
	}
	first, err := urlParseQuery(reviewRequests[0].Query)
	if err != nil {
		t.Fatal(err)
	}
	if first["startDate"] != "1/1/2000" || first["endDate"] != "7/13/2026" {
		t.Fatalf("date query = %+v", first)
	}
	if first["filter"] != "(rating ge 4) and market eq 'US'" || first["orderby"] != "date desc" {
		t.Fatalf("filter/order query = %+v", first)
	}
}

func TestUsageErrorsExitTwoAsJSON(t *testing.T) {
	t.Parallel()
	stdout, stderr, exitCode := execute(t, nil, []string{"--output", "json", "app", "info"}, cli.BuildInfo{})
	if stdout != "" || exitCode != output.ExitUsage || !json.Valid([]byte(strings.TrimSpace(stderr))) {
		t.Fatalf("stdout = %q stderr = %q exit = %d", stdout, stderr, exitCode)
	}
	_, _, exitCode = execute(t, config.Environment{}, []string{"reviews", "list", "--top", "10001"}, cli.BuildInfo{})
	if exitCode != output.ExitUsage {
		t.Fatalf("invalid top exit = %d, want 2", exitCode)
	}
}

func execute(t *testing.T, environment config.Environment, args []string, build cli.BuildInfo) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exitCode := cli.Execute(context.Background(), args, cli.Dependencies{
		Stdout:      &stdout,
		Stderr:      &stderr,
		Environment: environment,
		IsTTY:       false,
		Now:         func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) },
		Build:       build,
		Clock:       instantClock{},
		Rand:        cliFixedRand(1),
	})
	return stdout.String(), stderr.String(), exitCode
}

type instantClock struct{}

func (instantClock) Sleep(context.Context, time.Duration) error { return nil }
func (instantClock) Now() time.Time                             { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) }

type cliFixedRand float64

func (r cliFixedRand) Float64() float64 { return float64(r) }

func fullServer(t *testing.T) *fakestore.Server {
	t.Helper()
	return fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{
			ID:                                 "APP",
			Name:                               "Example",
			LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published", Status: "Published"},
			PendingApplicationSubmission:       &fakestore.SubmissionRef{ID: "pending", Status: "PendingCommit", StatusDetails: json.RawMessage(`{"warnings":["warning"]}`)},
		},
		Submissions: map[string]json.RawMessage{
			"published": json.RawMessage(`{"id":"published","status":"Published","listings":{"en-us":{"baseListing":{}},"it":{"baseListing":{}}}}`),
			"pending":   json.RawMessage(`{"id":"pending","status":"PendingCommit","statusDetails":{"warnings":["warning"]},"fileUploadUrl":"https://blob.example/upload?sig=sas-secret","listings":{"en-us":{},"it":{}}}`),
		},
		Rollouts: map[string]fakestore.Rollout{
			"published": {IsPackageRollout: true, PackageRolloutPercentage: 90, PackageRolloutStatus: "PackageRolloutInProgress", FallbackSubmissionID: "fallback"},
		},
		ReviewPages: []fakestore.ReviewPage{
			{Value: []json.RawMessage{json.RawMessage(`{"id":"review-one","date":"2026-07-13","market":"US","rating":5}`)}, TotalCount: 2},
			{Value: []json.RawMessage{json.RawMessage(`{"id":"review-two","date":"2026-07-12","market":"IT","rating":4}`)}, TotalCount: 2},
		},
	})
}

func fakeEnvironment(server *fakestore.Server) config.Environment {
	return config.Environment{
		"MS_STORE_TENANT_ID":     "tenant",
		"MS_STORE_CLIENT_ID":     "client",
		"MS_STORE_CLIENT_SECRET": "secret",
		"MS_STORE_APP_ID":        "APP",
		"PCENTER_API_BASE":       server.APIBase(),
		"PCENTER_LOGIN_BASE":     server.LoginBase(),
	}
}

func urlParseQuery(raw string) (map[string]string, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.invalid/?"+raw, nil)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for key := range request.URL.Query() {
		result[key] = request.URL.Query().Get(key)
	}
	return result, nil
}
