package submission_test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prof18/pcenter-cli/internal/fakestore"
	"github.com/prof18/pcenter-cli/internal/metadata"
	"github.com/prof18/pcenter-cli/internal/store"
	"github.com/prof18/pcenter-cli/internal/submission"
)

func TestListingPublisherCreatesDraftPreservesPackagesAndUploadsImages(t *testing.T) {
	t.Parallel()
	fixture := newListingFixture(t, listingFixtureOptions{})
	result, err := fixture.publisher.Push(context.Background(), submission.ListingPushOptions{
		AppID: "APP", MetadataDir: fixture.metadataDir, Directory: fixture.directory, SkipCommit: true,
		Poll: submission.PollOptions{Interval: 0, Attempts: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SubmissionID != "created" || !result.Draft || result.NextCommand != "pcenter submission commit" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Plan.ImageChanges) != 1 || result.Plan.ImageChanges[0].Action != "add" {
		t.Fatalf("plan = %+v", result.Plan)
	}

	var putBody map[string]json.RawMessage
	for _, request := range fixture.server.Journal() {
		if request.Method == http.MethodPut && strings.HasSuffix(request.Path, "/submissions/created") {
			if err := json.Unmarshal(request.Body, &putBody); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertJSONRawEqual(t, putBody["applicationPackages"], json.RawMessage(`[{"fileName":"app.msix","fileStatus":"Uploaded","version":"1.0.0.0","future":true}]`))
	assertJSONRawEqual(t, putBody["packageDeliveryOptions"], json.RawMessage(`{"isMandatoryUpdate":false,"packageRollout":{"isPackageRollout":false},"future":9}`))

	uploads := fixture.server.BlobUploads()
	if len(uploads) != 1 {
		t.Fatalf("uploads = %d", len(uploads))
	}
	reader, err := zip.NewReader(strings.NewReader(string(uploads[0])), int64(len(uploads[0])))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 1 || reader.File[0].Name != "en-us/new.png" || reader.File[0].Method != zip.Store {
		t.Fatalf("ZIP entries = %+v", reader.File)
	}
}

func TestListingPublisherCleansDraftAfterUploadFailure(t *testing.T) {
	t.Parallel()
	fixture := newListingFixture(t, listingFixtureOptions{uploader: failingUploader{}})
	_, err := fixture.publisher.Push(context.Background(), submission.ListingPushOptions{
		AppID: "APP", MetadataDir: fixture.metadataDir, Directory: fixture.directory, SkipCommit: true,
		Poll: submission.PollOptions{Interval: 0, Attempts: 2},
	})
	if err == nil || !strings.Contains(err.Error(), "upload failed") {
		t.Fatalf("error = %v", err)
	}
	for _, request := range fixture.server.Journal() {
		if request.Method == http.MethodDelete && strings.HasSuffix(request.Path, "/submissions/created") {
			return
		}
	}
	t.Fatal("created draft was not deleted after upload failure")
}

func TestListingPublisherPreflightsBeforeCreatingDraft(t *testing.T) {
	t.Parallel()
	fixture := newListingFixture(t, listingFixtureOptions{})
	if err := os.Remove(filepath.Join(fixture.metadataDir, "images", "en-us", "new.png")); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.publisher.Push(context.Background(), submission.ListingPushOptions{
		AppID: "APP", MetadataDir: fixture.metadataDir, Directory: fixture.directory, SkipCommit: true,
		Poll: submission.PollOptions{Interval: 0, Attempts: 2},
	})
	if err == nil || !strings.Contains(err.Error(), "new.png") {
		t.Fatalf("error = %v", err)
	}
	for _, request := range fixture.server.Journal() {
		if request.Method == http.MethodPost && strings.HasSuffix(request.Path, "/submissions") {
			t.Fatal("invalid metadata created a draft before validation")
		}
	}
}

func TestListingPublisherYesCommits(t *testing.T) {
	t.Parallel()
	fixture := newListingFixture(t, listingFixtureOptions{})
	result, err := fixture.publisher.Push(context.Background(), submission.ListingPushOptions{
		AppID: "APP", MetadataDir: fixture.metadataDir, Directory: fixture.directory,
		Poll: submission.PollOptions{Interval: 0, Attempts: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Draft || !result.Commit.Accepted {
		t.Fatalf("result = %+v", result)
	}
	for _, request := range fixture.server.Journal() {
		if request.Method == http.MethodPost && strings.HasSuffix(request.Path, "/submissions/created/commit") {
			return
		}
	}
	t.Fatal("--yes flow did not commit")
}

func TestListingPublisherAppliesReleaseNotes(t *testing.T) {
	t.Parallel()
	fixture := newListingFixture(t, listingFixtureOptions{})
	notesPath := filepath.Join(t.TempDir(), "notes.json")
	if err := os.WriteFile(notesPath, []byte(`{"notes":{"en-us":"New notes"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.publisher.Push(context.Background(), submission.ListingPushOptions{
		AppID: "APP", MetadataDir: fixture.metadataDir, Directory: fixture.directory,
		ReleaseNotesPath: notesPath, SkipCommit: true, Poll: submission.PollOptions{Interval: 0, Attempts: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range fixture.server.Journal() {
		if request.Method == http.MethodPut && strings.HasSuffix(request.Path, "/submissions/created") {
			if !strings.Contains(string(request.Body), `"releaseNotes":"New notes"`) {
				t.Fatalf("PUT body = %s", request.Body)
			}
			return
		}
	}
	t.Fatal("missing listing PUT")
}

func TestListingPublisherRequiresExplicitPendingReplacement(t *testing.T) {
	t.Parallel()
	blocked := newListingFixture(t, listingFixtureOptions{withPending: true})
	_, err := blocked.publisher.Push(context.Background(), submission.ListingPushOptions{
		AppID: "APP", MetadataDir: blocked.metadataDir, Directory: blocked.directory, SkipCommit: true,
		Poll: submission.PollOptions{Interval: 0, Attempts: 2},
	})
	if err == nil || !strings.Contains(err.Error(), "--replace-pending") {
		t.Fatalf("error = %v", err)
	}

	replaced := newListingFixture(t, listingFixtureOptions{withPending: true})
	result, err := replaced.publisher.Push(context.Background(), submission.ListingPushOptions{
		AppID: "APP", MetadataDir: replaced.metadataDir, Directory: replaced.directory,
		SkipCommit: true, ReplacePending: true, Poll: submission.PollOptions{Interval: 0, Attempts: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SubmissionID != "created" {
		t.Fatalf("result = %+v", result)
	}
	for _, request := range replaced.server.Journal() {
		if request.Method == http.MethodDelete && strings.HasSuffix(request.Path, "/submissions/draft") {
			return
		}
	}
	t.Fatal("pending draft was not deleted")
}

type listingFixture struct {
	server      *fakestore.Server
	publisher   *submission.ListingPublisher
	metadataDir string
	directory   metadata.Directory
}

type listingFixtureOptions struct {
	uploader    submission.BlobUploader
	withPending bool
}

func newListingFixture(t *testing.T, options listingFixtureOptions) listingFixture {
	t.Helper()
	dir := t.TempDir()
	metadataDir := filepath.Join(dir, "metadata")
	imagePath := filepath.Join(metadataDir, "images", "en-us", "new.png")
	writeListingPNG(t, imagePath)
	directory := metadata.Directory{
		Marker: metadata.StoreMarker{AppID: "APP"},
		Listings: map[string]metadata.Listing{"en-us": {
			Title: "Changed", Description: "Description", Features: []string{}, Keywords: []string{},
			RecommendedHardware: json.RawMessage(`[]`), MinimumHardware: json.RawMessage(`[]`),
		}},
		Images: metadata.ImageManifest{Images: map[string][]metadata.ImageEntry{"en-us": {
			{ImageType: "Screenshot", StoreID: "remote", RemoteOnly: true},
			{LocalPath: "en-us/new.png", ImageType: "Screenshot", Description: "New"},
		}}},
	}
	created := json.RawMessage(`{
		"id":"published","status":"Published",
		"listings":{"en-us":{"baseListing":{"title":"Old","description":"Description","features":[],"keywords":[],
		"recommendedHardware":[],"minimumHardware":[],"images":[{"fileName":"legacy.png","fileStatus":"Uploaded","id":"remote","imageType":"Screenshot"}]}}},
		"applicationPackages":[{"fileName":"app.msix","fileStatus":"Uploaded","version":"1.0.0.0","future":true}],
		"packageDeliveryOptions":{"isMandatoryUpdate":false,"packageRollout":{"isPackageRollout":false},"future":9}
	}`)
	app := fakestore.App{ID: "APP", LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published", Status: "Published"}}
	submissions := map[string]json.RawMessage{"published": created}
	if options.withPending {
		app.PendingApplicationSubmission = &fakestore.SubmissionRef{ID: "draft", Status: "PendingCommit"}
		submissions["draft"] = json.RawMessage(`{"id":"draft","status":"PendingCommit"}`)
	}
	server := fakestore.New(t, fakestore.Options{
		AppID:              "APP",
		App:                app,
		Submissions:        submissions,
		Rollouts:           map[string]fakestore.Rollout{"published": {PackageRolloutStatus: "PackageRolloutCompleted"}},
		CreateSubmissionID: "created", CreateSubmission: created, BlobEnabled: true,
	})
	clock := &fakeClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
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
	if options.uploader == nil {
		options.uploader = submission.NewAzureBlobUploader()
	}
	publisher, err := submission.NewListingPublisher(submission.ListingPublisherOptions{
		Client: client, Manager: manager, Uploader: options.uploader, TempDir: filepath.Join(dir, "tmp"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return listingFixture{server: server, publisher: publisher, metadataDir: metadataDir, directory: directory}
}

func writeListingPNG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 1366, 768))); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertJSONRawEqual(t *testing.T, actual, expected json.RawMessage) {
	t.Helper()
	var actualValue, expectedValue any
	if err := json.Unmarshal(actual, &actualValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expected, &expectedValue); err != nil {
		t.Fatal(err)
	}
	actualJSON, _ := json.Marshal(actualValue)
	expectedJSON, _ := json.Marshal(expectedValue)
	if string(actualJSON) != string(expectedJSON) {
		t.Fatalf("JSON = %s, want %s", actual, expected)
	}
}
