package fakestore_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/fakestore"
)

func TestMutationFailuresCanApplyServerSideState(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{
			ID:                                 "APP",
			LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published"},
			PendingApplicationSubmission:       &fakestore.SubmissionRef{ID: "draft"},
		},
		Submissions: map[string]json.RawMessage{
			"published": json.RawMessage(`{"id":"published","status":"Published"}`),
			"draft":     json.RawMessage(`{"id":"draft","status":"PendingCommit"}`),
		},
		Rollouts: map[string]fakestore.Rollout{
			"published": {IsPackageRollout: true, PackageRolloutPercentage: 90, PackageRolloutStatus: "PackageRolloutInProgress"},
		},
		CreateSubmissionID: "created",
		FinalizeScenario:   fakestore.MutationScenario{Failures: 1, Status: http.StatusGatewayTimeout, ApplyOnFailure: true},
		CreateScenario:     fakestore.MutationScenario{Failures: 1, Status: http.StatusGatewayTimeout, ApplyOnFailure: true},
		CommitScenario:     fakestore.MutationScenario{Failures: 1, Status: http.StatusGatewayTimeout, ApplyOnFailure: true},
		DeleteScenario:     fakestore.MutationScenario{Failures: 1, Status: http.StatusGatewayTimeout, ApplyOnFailure: true},
	})

	assertGatewayTimeout(t, http.MethodPost, server.APIBase()+"/applications/APP/submissions/published/finalizepackagerollout")
	assertGETJSON(t, server.APIBase()+"/applications/APP/submissions/published/packagerollout", "PackageRolloutCompleted")

	// Remove the initial draft before exercising create.
	assertGatewayTimeout(t, http.MethodDelete, server.APIBase()+"/applications/APP/submissions/draft")
	assertGETJSONNotContains(t, server.APIBase()+"/applications/APP", "draft")

	assertGatewayTimeout(t, http.MethodPost, server.APIBase()+"/applications/APP/submissions")
	assertGETJSON(t, server.APIBase()+"/applications/APP", "created")

	assertGatewayTimeout(t, http.MethodPost, server.APIBase()+"/applications/APP/submissions/created/commit")
	assertGETJSON(t, server.APIBase()+"/applications/APP/submissions/created/status", "CommitStarted")
}

func TestMutationFailureCanLeaveStateUnchanged(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{
			ID:                                 "APP",
			LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published"},
		},
		Rollouts: map[string]fakestore.Rollout{
			"published": {IsPackageRollout: true, PackageRolloutPercentage: 90, PackageRolloutStatus: "PackageRolloutInProgress"},
		},
		FinalizeScenario: fakestore.MutationScenario{Failures: 1, Status: http.StatusGatewayTimeout, ApplyOnFailure: false},
	})
	assertGatewayTimeout(t, http.MethodPost, server.APIBase()+"/applications/APP/submissions/published/finalizepackagerollout")
	assertGETJSON(t, server.APIBase()+"/applications/APP/submissions/published/packagerollout", "PackageRolloutInProgress")
}

func assertGatewayTimeout(t *testing.T, method, endpoint string) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, endpoint, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("%s %s status = %d, want %d", method, endpoint, response.StatusCode, http.StatusGatewayTimeout)
	}
}

func assertGETJSONNotContains(t *testing.T, endpoint, unwanted string) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), unwanted) {
		t.Fatalf("GET %s body contains %q: %s", endpoint, unwanted, body)
	}
}
