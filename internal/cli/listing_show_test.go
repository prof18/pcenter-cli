package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/cli"
	"github.com/prof18/pcenter-cli/internal/fakestore"
	"github.com/prof18/pcenter-cli/internal/output"
)

func showServer(t *testing.T) *fakestore.Server {
	t.Helper()
	published := `{"id":"published","status":"Published","listings":{
		"en-us":{"baseListing":{"title":"Example","description":"Long description","features":["one","two"],"keywords":["k1"],
			"images":[{"fileName":"en-us/a.png","fileStatus":"Uploaded","id":"img-1","imageType":"Screenshot","description":"a caption"}]}},
		"it":{"baseListing":{"title":"Esempio","description":"Descrizione","features":[],"keywords":[]}}}}`
	pending := `{"id":"pending","status":"PendingCommit","listings":{
		"en-us":{"baseListing":{"title":"Draft title","description":"Draft description","features":[],"keywords":[]}}}}`
	return fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{
			ID: "APP", PrimaryName: "Example",
			LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published"},
			PendingApplicationSubmission:       &fakestore.SubmissionRef{ID: "pending"},
		},
		Submissions: map[string]json.RawMessage{
			"published": json.RawMessage(published),
			"pending":   json.RawMessage(pending),
		},
	})
}

func TestListingShowReadsWithoutWritingAnything(t *testing.T) {
	t.Parallel()
	server := showServer(t)
	environment := fakeEnvironment(server)

	stdout, stderr, exitCode := execute(t, environment, []string{"--output", "json", "listing", "show"}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess || stderr != "" {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	var result struct {
		Source       string `json:"source"`
		SubmissionID string `json:"submissionId"`
		LocaleCount  int    `json:"localeCount"`
		Listings     map[string]struct {
			Title      string   `json:"title"`
			Features   []string `json:"features"`
			ImageCount int      `json:"imageCount"`
			Images     []struct {
				Description string `json:"description"`
			} `json:"images"`
		} `json:"listings"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("not JSON: %s", stdout)
	}
	if result.Source != "published" || result.SubmissionID != "published" || result.LocaleCount != 2 {
		t.Fatalf("unexpected envelope: %+v", result)
	}
	if result.Listings["en-us"].Title != "Example" || len(result.Listings["en-us"].Features) != 2 {
		t.Fatalf("en-us listing wrong: %+v", result.Listings["en-us"])
	}
	// Image count is always available; the entries only with --images, because
	// the binaries themselves cannot be fetched through the API.
	if result.Listings["en-us"].ImageCount != 1 {
		t.Fatalf("image count = %d, want 1", result.Listings["en-us"].ImageCount)
	}
	if len(result.Listings["en-us"].Images) != 0 {
		t.Fatalf("images should be omitted without --images: %+v", result.Listings["en-us"].Images)
	}

	stdout, _, exitCode = execute(t, environment, []string{"--output", "json", "listing", "show", "--images"}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess {
		t.Fatalf("--images exit = %d", exitCode)
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("not JSON: %s", stdout)
	}
	if len(result.Listings["en-us"].Images) != 1 || result.Listings["en-us"].Images[0].Description != "a caption" {
		t.Fatalf("--images should carry captions: %+v", result.Listings["en-us"].Images)
	}
}

func TestListingShowLocaleAndSourceSelection(t *testing.T) {
	t.Parallel()
	server := showServer(t)
	environment := fakeEnvironment(server)

	stdout, _, exitCode := execute(t, environment, []string{"--output", "json", "listing", "show", "--locale", "IT"}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess {
		t.Fatalf("exit = %d", exitCode)
	}
	// Locale matching is case-insensitive, as everywhere else in the tool.
	if !strings.Contains(stdout, "Esempio") || strings.Contains(stdout, "Example") {
		t.Fatalf("--locale IT should return only the it listing: %s", stdout)
	}

	stdout, _, exitCode = execute(t, environment, []string{"--output", "json", "listing", "show", "--pending"}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess {
		t.Fatalf("--pending exit = %d", exitCode)
	}
	if !strings.Contains(stdout, "Draft title") || !strings.Contains(stdout, `"source":"pending"`) {
		t.Fatalf("--pending should read the draft: %s", stdout)
	}

	// An unknown locale names the mistake rather than returning an empty object
	// that a caller might read as "the listing is empty".
	_, stderr, exitCode := execute(t, environment, []string{"--output", "json", "listing", "show", "--locale", "zz"}, cli.BuildInfo{})
	if exitCode == output.ExitSuccess {
		t.Fatal("unknown locale should fail")
	}
	if !strings.Contains(stderr, "locales list") {
		t.Fatalf("error should point at locales list: %s", stderr)
	}

	_, _, exitCode = execute(t, environment, []string{"listing", "show", "--published", "--pending"}, cli.BuildInfo{})
	if exitCode != output.ExitUsage {
		t.Fatalf("conflicting source flags exit = %d, want %d", exitCode, output.ExitUsage)
	}
}
