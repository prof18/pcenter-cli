package store_test

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

func TestClientRetriesTokenHonorsRetryAfterAndRefreshesOnceOn401(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App:   fakestore.App{ID: "APP", PrimaryName: "Example"},
		Failures: []fakestore.Failure{
			{Method: http.MethodPost, Path: "/tenant/oauth2/token", Status: http.StatusTooManyRequests, RetryAfter: "17", Count: 1},
			{Method: http.MethodGet, Path: "/v1.0/my/applications/APP", Status: http.StatusUnauthorized, Count: 1},
		},
	})
	clock := &fakeClock{now: time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)}
	client := newTestClient(t, server, clock)

	app, err := client.Application(context.Background(), "APP")
	if err != nil {
		t.Fatal(err)
	}
	if app.PrimaryName != "Example" {
		t.Fatalf("app name = %q", app.PrimaryName)
	}
	if got := clock.Sleeps(); len(got) != 1 || got[0] != 17*time.Second {
		t.Fatalf("sleeps = %v, want [17s]", got)
	}

	journal := server.Journal()
	var tokenRequests int
	for _, request := range journal {
		if request.Method == http.MethodPost && strings.HasSuffix(request.Path, "/oauth2/token") {
			tokenRequests++
		}
	}
	if tokenRequests != 3 {
		t.Fatalf("token requests = %d, want 3 (retry then one refresh)", tokenRequests)
	}
}

func TestClientRetriesGETWithExponentialBackoff(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App:   fakestore.App{ID: "APP"},
		Failures: []fakestore.Failure{
			{Method: http.MethodGet, Path: "/v1.0/my/applications/APP", Status: http.StatusServiceUnavailable, Count: 2},
		},
	})
	clock := &fakeClock{now: time.Now()}
	client := newTestClient(t, server, clock)
	if _, err := client.Application(context.Background(), "APP"); err != nil {
		t.Fatal(err)
	}
	if got := clock.Sleeps(); len(got) != 2 || got[0] != 5*time.Second || got[1] != 10*time.Second {
		t.Fatalf("sleeps = %v, want [5s 10s]", got)
	}
}

func TestClientSecond401IsPermanent(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App:   fakestore.App{ID: "APP"},
		Failures: []fakestore.Failure{
			{Method: http.MethodGet, Path: "/v1.0/my/applications/APP", Status: http.StatusUnauthorized, Count: 2},
		},
	})
	client := newTestClient(t, server, &fakeClock{now: time.Now()})
	_, err := client.Application(context.Background(), "APP")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v, want permanent 401", err)
	}
}

func TestClientErrorIncludesBodyAndCorrelationButRedactsSecrets(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App:   fakestore.App{ID: "APP"},
		Failures: []fakestore.Failure{{
			Method: http.MethodGet,
			Path:   "/v1.0/my/applications/APP",
			Status: http.StatusBadRequest,
			Body:   `{"message":"diagnostic https://blob.example/a?sig=sas-secret client-secret"}`,
			Count:  1,
		}},
	})
	client := newTestClient(t, server, &fakeClock{now: time.Now()})
	_, err := client.Application(context.Background(), "APP")
	if err == nil {
		t.Fatal("request unexpectedly succeeded")
	}
	message := err.Error()
	for _, expected := range []string{"diagnostic", "correlation-test", "400"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("error missing %q: %s", expected, message)
		}
	}
	for _, secret := range []string{"sas-secret", "client-secret", "sig="} {
		if strings.Contains(message, secret) {
			t.Fatalf("error leaked %q: %s", secret, message)
		}
	}
}

func TestClientReadsSubmissionRolloutAndReviews(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{
			ID:                                 "APP",
			LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published"},
		},
		Submissions: map[string]json.RawMessage{"published": json.RawMessage(`{"id":"published","status":"Published"}`)},
		Rollouts: map[string]fakestore.Rollout{
			"published": {IsPackageRollout: true, PackageRolloutPercentage: 90, PackageRolloutStatus: "PackageRolloutInProgress", FallbackSubmissionID: "fallback"},
		},
		ReviewPages: []fakestore.ReviewPage{{Value: []json.RawMessage{json.RawMessage(`{"id":"review","rating":5}`)}, TotalCount: 1}},
	})
	client := newTestClient(t, server, &fakeClock{now: time.Now()})

	if submission, err := client.Submission(context.Background(), "APP", "published"); err != nil || submission.Status != "Published" {
		t.Fatalf("submission = %+v, error = %v", submission, err)
	}
	if rollout, err := client.Rollout(context.Background(), "APP", "published"); err != nil || rollout.FallbackSubmissionID != "fallback" {
		t.Fatalf("rollout = %+v, error = %v", rollout, err)
	}
	page, err := client.Reviews(context.Background(), store.ReviewQuery{ApplicationID: "APP", Top: 10000, Skip: 0})
	if err != nil || len(page.Value) != 1 || page.TotalCount != 1 {
		t.Fatalf("reviews = %+v, error = %v", page, err)
	}
}

func TestClientRetriesPUTButNeverBlindRetriesPOST(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{
		Failures: []fakestore.Failure{
			{Method: http.MethodPut, Path: "/v1.0/my/echo", Status: http.StatusServiceUnavailable, Count: 2},
			{Method: http.MethodPost, Path: "/v1.0/my/echo", Status: http.StatusServiceUnavailable, Count: 1},
		},
		Responses: []fakestore.Response{
			{Method: http.MethodPut, Path: "/v1.0/my/echo", Status: http.StatusOK},
			{Method: http.MethodPost, Path: "/v1.0/my/echo", Status: http.StatusOK},
		},
	})
	clock := &fakeClock{now: time.Now()}
	client := newTestClient(t, server, clock)
	if _, err := client.DoJSON(context.Background(), http.MethodPut, "/echo", json.RawMessage(`{"value":1}`)); err != nil {
		t.Fatal(err)
	}
	if got := clock.Sleeps(); len(got) != 2 || got[0] != 5*time.Second || got[1] != 10*time.Second {
		t.Fatalf("PUT sleeps = %v", got)
	}
	if _, err := client.DoJSON(context.Background(), http.MethodPost, "/echo", nil); err == nil {
		t.Fatal("POST transient failure was unexpectedly retried")
	}
	var postRequests int
	for _, request := range server.Journal() {
		if request.Method == http.MethodPost && request.Path == "/v1.0/my/echo" {
			postRequests++
		}
	}
	if postRequests != 1 {
		t.Fatalf("POST requests = %d, want 1", postRequests)
	}
}

func TestClientSendsEmptyJSONBodyForBodylessNonGET(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{
		Responses: []fakestore.Response{{Method: http.MethodPost, Path: "/v1.0/my/bodyless", Status: http.StatusOK}},
	})
	client := newTestClient(t, server, &fakeClock{now: time.Now()})
	if _, err := client.DoJSON(context.Background(), http.MethodPost, "/bodyless", nil); err != nil {
		t.Fatal(err)
	}
	journal := server.Journal()
	request := journal[len(journal)-1]
	if request.ContentType != "application/json" || len(request.Body) != 0 {
		t.Fatalf("bodyless request content type = %q body = %q", request.ContentType, request.Body)
	}
}

func newTestClient(t *testing.T, server *fakestore.Server, clock *fakeClock) *store.Client {
	t.Helper()
	client, err := store.NewClient(store.ClientOptions{
		APIBase:       server.APIBase(),
		LoginBase:     server.LoginBase(),
		TenantID:      "tenant",
		ClientID:      "client",
		ClientSecret:  "client-secret",
		HTTPClient:    http.DefaultClient,
		Clock:         clock,
		Rand:          testRand(1),
		CorrelationID: "correlation-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type testRand float64

func (r testRand) Float64() float64 { return float64(r) }
