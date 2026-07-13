package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/cli"
	"github.com/prof18/pcenter-cli/internal/fakestore"
	"github.com/prof18/pcenter-cli/internal/output"
)

func TestPublishMSIXCommandPreparesSkipCommitDraft(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	msix := filepath.Join(dir, "FeedFlow.msix")
	if err := os.WriteFile(msix, []byte("msix"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := fakestore.New(t, fakestore.Options{
		AppID:              "APP",
		App:                fakestore.App{ID: "APP", LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published", Status: "Published"}},
		Submissions:        map[string]json.RawMessage{"published": json.RawMessage(`{"id":"published","status":"Published"}`)},
		Rollouts:           map[string]fakestore.Rollout{"published": {PackageRolloutStatus: "PackageRolloutCompleted"}},
		CreateSubmissionID: "created",
		CreateSubmission: json.RawMessage(`{
          "listings":{"en-us":{"baseListing":{"releaseNotes":"old"}}},
          "applicationPackages":[{"fileName":"old.msix","version":"1.0.0.0","fileStatus":"Uploaded"}]
        }`),
		BlobEnabled: true,
	})
	stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{
		"--output", "json", "publish", "msix", "--path", msix,
		"--keep-existing-release-notes", "--rollout-percentage", "90", "--skip-commit",
	}, cli.BuildInfo{})
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, `"draft":true`) || !strings.Contains(stdout, "pcenter submission commit") {
		t.Fatalf("stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
	}
	if len(server.BlobUploads()) != 1 {
		t.Fatalf("uploads = %d", len(server.BlobUploads()))
	}
}

func TestPublishMSIXCommandRequiresReleaseNoteIntent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "app.msix")
	if err := os.WriteFile(path, []byte("msix"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exitCode := execute(t, nil, []string{"--output", "json", "publish", "msix", "--path", path}, cli.BuildInfo{})
	if exitCode != output.ExitUsage || stdout != "" || !strings.Contains(stderr, "exactly one") {
		t.Fatalf("stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
	}
}
