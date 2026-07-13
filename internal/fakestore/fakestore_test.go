package fakestore_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/fakestore"
)

func TestServerExposesTokenAndReadOnlyStoreEndpoints(t *testing.T) {
	t.Parallel()

	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{
			ID:                                 "APP",
			Name:                               "Example",
			LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published", Status: "Published"},
			PendingApplicationSubmission:       &fakestore.SubmissionRef{ID: "pending", Status: "PendingCommit"},
		},
		Submissions: map[string]json.RawMessage{
			"pending": json.RawMessage(`{"id":"pending","status":"PendingCommit","fileUploadUrl":"https://blob.invalid/upload?sig=secret"}`),
		},
		Rollouts: map[string]fakestore.Rollout{
			"published": {IsPackageRollout: true, PackageRolloutPercentage: 90, PackageRolloutStatus: "PackageRolloutInProgress", FallbackSubmissionID: "fallback"},
		},
		ReviewPages: []fakestore.ReviewPage{
			{Value: []json.RawMessage{json.RawMessage(`{"id":"one","rating":5}`)}, TotalCount: 2},
			{Value: []json.RawMessage{json.RawMessage(`{"id":"two","rating":4}`)}, TotalCount: 2},
		},
	})

	form := url.Values{"client_id": {"client"}, "client_secret": {"secret"}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.LoginBase()+"/tenant/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d", resp.StatusCode)
	}

	assertGETJSON(t, server.APIBase()+"/applications/APP", "Example")
	assertGETJSON(t, server.APIBase()+"/applications/APP/submissions/pending", "PendingCommit")
	assertGETJSON(t, server.APIBase()+"/applications/APP/submissions/published/packagerollout", "fallback")
	assertGETJSON(t, server.APIBase()+"/analytics/reviews?applicationId=APP&skip=0", "one")
	assertGETJSON(t, server.APIBase()+"/analytics/reviews?applicationId=APP&skip=1", "two")

	journal := server.Journal()
	if len(journal) != 6 {
		t.Fatalf("journal length = %d, want 6", len(journal))
	}
	if !journal[1].AuthorizationPresent {
		t.Fatal("Store request did not include Authorization")
	}
}

func TestServerInjectsFailuresAndRecordsHeaders(t *testing.T) {
	t.Parallel()

	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App:   fakestore.App{ID: "APP"},
		Failures: []fakestore.Failure{
			{Method: http.MethodGet, Path: "/v1.0/my/applications/APP", Status: http.StatusTooManyRequests, RetryAfter: "17", Count: 1},
			{Method: http.MethodGet, Path: "/v1.0/my/applications/APP", Status: http.StatusUnauthorized, Count: 1},
		},
	})

	for _, wantStatus := range []int{http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusOK} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.APIBase()+"/applications/APP", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("MS-CorrelationId", "correlation")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != wantStatus {
			t.Fatalf("status = %d, want %d", resp.StatusCode, wantStatus)
		}
	}

	journal := server.Journal()
	if !journal[0].CorrelationIDPresent || !journal[0].AuthorizationPresent {
		t.Fatalf("headers not recorded: %+v", journal[0])
	}
}

func assertGETJSON(t *testing.T, endpoint, contains string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d body = %s", endpoint, resp.StatusCode, body)
	}
	if !json.Valid(body) || !containsBytes(body, []byte(contains)) {
		t.Fatalf("GET %s body = %s, want JSON containing %q", endpoint, body, contains)
	}
}

func containsBytes(haystack, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}
