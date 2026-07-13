package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/cli"
	"github.com/prof18/pcenter-cli/internal/fakestore"
)

func TestListingPullWritesPublishedSnapshotByDefault(t *testing.T) {
	t.Parallel()
	server := listingPullServer(t)
	dir := t.TempDir()
	stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{
		"--output", "json", "listing", "pull", "--dir", dir,
	}, cli.BuildInfo{Version: "v1.2.3"})
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exit = %d stderr = %q stdout = %q", exitCode, stderr, stdout)
	}
	for _, expected := range []string{`"submissionId":"published"`, `"listingCount":1`, `"remoteOnlyImages":1`} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("output missing %s: %s", expected, stdout)
		}
	}
	storeData, err := os.ReadFile(filepath.Join(dir, "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(storeData), `"appId": "APP"`) || !strings.Contains(string(storeData), `"generatedBy": "pcenter v1.2.3"`) {
		t.Fatalf("store.json = %s", storeData)
	}
	listingData, err := os.ReadFile(filepath.Join(dir, "listings", "en-us.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(listingData), "releaseNotes") || !strings.Contains(string(listingData), `"title": "Published"`) {
		t.Fatalf("listing = %s", listingData)
	}
}

func TestListingPullCanSelectPendingAndRejectsConflictingSourceFlags(t *testing.T) {
	t.Parallel()
	server := listingPullServer(t)
	dir := t.TempDir()
	stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{
		"--output", "json", "listing", "pull", "--dir", dir, "--pending",
	}, cli.BuildInfo{})
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, `"submissionId":"pending"`) {
		t.Fatalf("exit = %d stderr = %q stdout = %q", exitCode, stderr, stdout)
	}
	_, _, exitCode = execute(t, fakeEnvironment(server), []string{
		"listing", "pull", "--dir", t.TempDir(), "--published", "--pending",
	}, cli.BuildInfo{})
	if exitCode != 2 {
		t.Fatalf("conflicting source flags exit = %d", exitCode)
	}
}

func listingPullServer(t *testing.T) *fakestore.Server {
	t.Helper()
	return fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{
			ID: "APP", LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published", Status: "Published"},
			PendingApplicationSubmission: &fakestore.SubmissionRef{ID: "pending", Status: "PendingCommit"},
		},
		Submissions: map[string]json.RawMessage{
			"published": json.RawMessage(`{"id":"published","status":"Published","listings":{"en-us":{"baseListing":{"title":"Published","description":"Description","features":[],"keywords":[],"recommendedHardware":[],"minimumHardware":[],"releaseNotes":"old","images":[{"fileName":"legacy.png","fileStatus":"Uploaded","id":"image","description":"Screenshot","imageType":"Screenshot"}]}}}}`),
			"pending":   json.RawMessage(`{"id":"pending","status":"PendingCommit","listings":{"it":{"baseListing":{"title":"Pending","description":"Description","features":[],"keywords":[],"recommendedHardware":[],"minimumHardware":[],"images":[]}}}}`),
		},
	})
}
