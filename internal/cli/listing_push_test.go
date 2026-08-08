package cli_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prof18/pcenter-cli/internal/cli"
	"github.com/prof18/pcenter-cli/internal/fakestore"
	"github.com/prof18/pcenter-cli/internal/metadata"
)

func TestListingPushDryRunReportsNoChangesWithoutMutation(t *testing.T) {
	t.Parallel()
	server, submission := listingPushServer(t)
	dir := t.TempDir()
	snapshot, _, err := metadata.SnapshotFromSubmission(dir, "APP", "test", time.Now(), submission)
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.WriteSnapshot(dir, snapshot); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{
		"--output", "json", "listing", "push", "--dir", dir, "--dry-run",
	}, cli.BuildInfo{})
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exit = %d stderr = %q stdout = %q", exitCode, stderr, stdout)
	}
	for _, expected := range []string{`"dryRun":true`, `"hasChanges":false`, `"applicationPackages"`, `"packageDeliveryOptions"`} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("output missing %s: %s", expected, stdout)
		}
	}
	for _, request := range server.Journal() {
		isStoreMutation := strings.HasPrefix(request.Path, "/v1.0/my/") &&
			(request.Method == http.MethodPost || request.Method == http.MethodPut || request.Method == http.MethodDelete)
		if isStoreMutation {
			t.Fatalf("dry-run mutated Store: %+v", request)
		}
	}
}

func TestListingPushRequiresExactlyOneModeAndIdentityGuard(t *testing.T) {
	t.Parallel()
	server, _ := listingPushServer(t)
	for _, args := range [][]string{
		{"listing", "push", "--dir", t.TempDir()},
		{"listing", "push", "--dir", t.TempDir(), "--dry-run", "--skip-commit"},
	} {
		_, _, exitCode := execute(t, fakeEnvironment(server), args, cli.BuildInfo{})
		if exitCode != 2 {
			t.Fatalf("args %v exit = %d", args, exitCode)
		}
	}
	dir := t.TempDir()
	if err := metadata.WriteSnapshot(dir, metadata.Snapshot{
		Marker: metadata.StoreMarker{AppID: "OTHER"},
		Listings: map[string]metadata.Listing{
			"en-us": {Title: "Wrong"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	_, stderr, exitCode := execute(t, fakeEnvironment(server), []string{
		"listing", "push", "--dir", dir, "--dry-run",
	}, cli.BuildInfo{})
	if exitCode != 1 || !strings.Contains(stderr, "belongs to app OTHER") {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
}

func TestListingPushSkipCommitCreatesDraft(t *testing.T) {
	t.Parallel()
	server, submission := listingPushServer(t)
	dir := t.TempDir()
	snapshot, _, err := metadata.SnapshotFromSubmission(dir, "APP", "test", time.Now(), submission)
	if err != nil {
		t.Fatal(err)
	}
	listing := snapshot.Listings["en-us"]
	listing.Title = "Changed"
	snapshot.Listings["en-us"] = listing
	if err := metadata.WriteSnapshot(dir, snapshot); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{
		"--output", "json", "listing", "push", "--dir", dir, "--skip-commit",
	}, cli.BuildInfo{})
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exit = %d stderr = %q stdout = %q", exitCode, stderr, stdout)
	}
	for _, expected := range []string{`"submissionId":"created"`, `"draft":true`, `"field":"title"`} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("output missing %s: %s", expected, stdout)
		}
	}
	for _, request := range server.Journal() {
		if request.Method == http.MethodPost && strings.HasSuffix(request.Path, "/submissions/created/commit") {
			t.Fatal("skip-commit committed the draft")
		}
	}
}

// A dry run creates nothing, so a pending submission is not an obstacle to it —
// only to the real push. Failing here would leave you unable to preview a change
// until you had resolved a draft, which is the wrong way round.
func TestListingPushDryRunPreviewsThroughAPendingSubmission(t *testing.T) {
	t.Parallel()
	submission := listingPushSubmission()
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{ID: "APP",
			LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published"},
			PendingApplicationSubmission:       &fakestore.SubmissionRef{ID: "draft"},
		},
		Submissions: map[string]json.RawMessage{
			"published": submission,
			"draft":     json.RawMessage(`{"id":"draft","status":"PendingCommit"}`),
		},
		CreateSubmissionID: "created", CreateSubmission: submission,
		Rollouts: map[string]fakestore.Rollout{"published": {PackageRolloutStatus: "PackageRolloutCompleted"}},
	})

	dir := t.TempDir()
	snapshot, _, err := metadata.SnapshotFromSubmission(dir, "APP", "test", time.Now(), submission)
	if err != nil {
		t.Fatal(err)
	}
	listing := snapshot.Listings["en-us"]
	listing.Title = "Changed"
	snapshot.Listings["en-us"] = listing
	if err := metadata.WriteSnapshot(dir, snapshot); err != nil {
		t.Fatal(err)
	}

	stdout, _, exitCode := execute(t, fakeEnvironment(server), []string{
		"--output", "json", "listing", "push", "--dir", dir, "--dry-run",
	}, cli.BuildInfo{})
	if exitCode != 0 {
		t.Fatalf("dry run should succeed despite a pending draft: exit = %d stdout = %q", exitCode, stdout)
	}

	var result struct {
		DryRun       bool     `json:"dryRun"`
		HasChanges   bool     `json:"hasChanges"`
		Warnings     []string `json:"warnings"`
		ImageChanges []struct {
			Locale string `json:"locale"`
		} `json:"imageChanges"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("output is not parseable: %s", stdout)
	}
	if !result.DryRun || !result.HasChanges {
		t.Fatalf("expected a diff to be reported: %s", stdout)
	}
	// The draft still blocks a real push, so saying so is the point of not failing.
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "draft") {
		t.Fatalf("expected one warning naming the pending draft, got %v", result.Warnings)
	}
	for _, request := range server.Journal() {
		isStoreMutation := strings.HasPrefix(request.Path, "/v1.0/my/") &&
			(request.Method == http.MethodPost || request.Method == http.MethodPut || request.Method == http.MethodDelete)
		if isStoreMutation {
			t.Fatalf("dry-run mutated Store: %+v", request)
		}
	}
}

// listingChanges renders as [] when empty; imageChanges must too. A caller doing
// len() on one and not the other is a bug we would be handing out.
func TestListingPushDryRunRendersEmptyChangeListsAsArrays(t *testing.T) {
	t.Parallel()
	server, submission := listingPushServer(t)
	dir := t.TempDir()
	snapshot, _, err := metadata.SnapshotFromSubmission(dir, "APP", "test", time.Now(), submission)
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.WriteSnapshot(dir, snapshot); err != nil {
		t.Fatal(err)
	}

	stdout, _, exitCode := execute(t, fakeEnvironment(server), []string{
		"--output", "json", "listing", "push", "--dir", dir, "--dry-run",
	}, cli.BuildInfo{})
	if exitCode != 0 {
		t.Fatalf("exit = %d stdout = %q", exitCode, stdout)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("output is not parseable: %s", stdout)
	}
	for _, field := range []string{"listingChanges", "imageChanges"} {
		value, present := raw[field]
		if !present {
			t.Fatalf("%s is absent from %s", field, stdout)
		}
		if string(value) != "[]" {
			t.Fatalf("%s = %s, want [] so that len() is always safe", field, value)
		}
	}
}

func listingPushServer(t *testing.T) (*fakestore.Server, json.RawMessage) {
	t.Helper()
	submission := listingPushSubmission()
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{ID: "APP", LastPublishedApplicationSubmission: &fakestore.SubmissionRef{
			ID: "published",
		}},
		Submissions:        map[string]json.RawMessage{"published": submission},
		CreateSubmissionID: "created", CreateSubmission: submission,
		Rollouts: map[string]fakestore.Rollout{"published": {
			PackageRolloutStatus: "PackageRolloutCompleted",
		}},
	})
	return server, submission
}

func listingPushSubmission() json.RawMessage {
	return json.RawMessage(`{
		"id":"published","status":"Published","targetPublishMode":"Immediate",
		"applicationPackages":[{"fileName":"app.msix","fileStatus":"Uploaded","version":"1.0.0.0"}],
		"packageDeliveryOptions":{"isMandatoryUpdate":false,"packageRollout":{"isPackageRollout":false}},
		"listings":{"en-us":{"baseListing":{"title":"Title","description":"Description","features":[],"keywords":[],
		"recommendedHardware":[],"minimumHardware":[],"images":[{"fileName":"legacy.png","fileStatus":"Uploaded","id":"image","imageType":"Screenshot"}]}}}
	}`)
}
