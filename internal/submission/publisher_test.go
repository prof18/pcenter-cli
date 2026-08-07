package submission_test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prof18/pcenter-cli/internal/fakestore"
	"github.com/prof18/pcenter-cli/internal/store"
	"github.com/prof18/pcenter-cli/internal/submission"
)

func TestPublishMSIXHappyPathMatchesPowerShellSequenceAndUpload(t *testing.T) {
	t.Parallel()
	fixture := newPublishFixture(t, publishFixtureOptions{rolloutInProgress: true})
	result, err := fixture.publisher.PublishMSIX(context.Background(), submission.PublishMSIXOptions{
		AppID: "APP", MSIXPath: fixture.msixPath, ReleaseNotesPath: fixture.notesPath,
		RolloutPercentage: 90, Poll: submission.PollOptions{Interval: 0, Attempts: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SubmissionID != "created" || result.Draft || !result.Commit.Accepted || result.Commit.Status != "PreProcessing" {
		t.Fatalf("result = %+v", result)
	}

	wantSequence := []string{
		"GET /v1.0/my/applications/APP",
		"GET /v1.0/my/applications/APP/submissions/published/packagerollout",
		"POST /v1.0/my/applications/APP/submissions/published/finalizepackagerollout",
		"GET /v1.0/my/applications/APP",
		"POST /v1.0/my/applications/APP/submissions",
		"PUT /v1.0/my/applications/APP/submissions/created",
		"PUT /blob/upload.zip",
		"POST /v1.0/my/applications/APP/submissions/created/commit",
		"GET /v1.0/my/applications/APP/submissions/created/status",
	}
	assertRequestSequence(t, fixture.server.Journal(), wantSequence)

	putRequest := findRequest(t, fixture.server.Journal(), http.MethodPut, "/submissions/created")
	for _, expected := range []string{`"releaseNotes":"English note"`, `"releaseNotes":"Prima\r\nSeconda"`, `"fileName":"FeedFlow.msix"`, `"packageRolloutPercentage":90`} {
		if !strings.Contains(string(putRequest.Body), expected) {
			t.Fatalf("PUT body missing %s: %s", expected, putRequest.Body)
		}
	}
	uploads := fixture.server.BlobUploads()
	if len(uploads) != 1 {
		t.Fatalf("uploads = %d", len(uploads))
	}
	reader, err := zip.NewReader(strings.NewReader(string(uploads[0])), int64(len(uploads[0])))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 1 || reader.File[0].Name != "FeedFlow.msix" || reader.File[0].Method != zip.Store {
		t.Fatalf("ZIP entries = %+v", reader.File)
	}
	entries, err := os.ReadDir(fixture.uploadTempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary ZIP was not cleaned up: %+v", entries)
	}
}

func TestPublishMSIXSkipCommitLeavesDraftAndReplacePendingDeletesFirst(t *testing.T) {
	t.Parallel()
	fixture := newPublishFixture(t, publishFixtureOptions{withPending: true})
	result, err := fixture.publisher.PublishMSIX(context.Background(), submission.PublishMSIXOptions{
		AppID: "APP", MSIXPath: fixture.msixPath, KeepExistingReleaseNotes: true,
		RolloutPercentage: 75, SkipCommit: true, ReplacePending: true,
		Poll: submission.PollOptions{Interval: 0, Attempts: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Draft || result.NextCommand != "pcenter submission commit" {
		t.Fatalf("result = %+v", result)
	}
	journal := fixture.server.Journal()
	if countJournalRequests(journal, http.MethodDelete, "/submissions/draft") != 1 || countJournalRequests(journal, http.MethodPost, "/commit") != 0 {
		t.Fatalf("journal = %+v", journal)
	}
	assertRequestSequence(t, journal, []string{
		"GET /v1.0/my/applications/APP",
		"GET /v1.0/my/applications/APP/submissions/published/packagerollout",
		"GET /v1.0/my/applications/APP",
		"GET /v1.0/my/applications/APP/submissions/draft",
		"DELETE /v1.0/my/applications/APP/submissions/draft",
		"GET /v1.0/my/applications/APP",
		"POST /v1.0/my/applications/APP/submissions",
		"PUT /v1.0/my/applications/APP/submissions/created",
		"PUT /blob/upload.zip",
	})
}

func TestPublishMSIXCleansUpDraftAfterPostCreateFailure(t *testing.T) {
	t.Parallel()
	fixture := newPublishFixture(t, publishFixtureOptions{})
	fixture.server.SetFailures([]fakestore.Failure{{
		Method: http.MethodPut, Path: "/v1.0/my/applications/APP/submissions/created", Status: http.StatusBadRequest,
		Body: `{"error":"bad payload"}`, Count: 1,
	}})
	_, err := fixture.publisher.PublishMSIX(context.Background(), submission.PublishMSIXOptions{
		AppID: "APP", MSIXPath: fixture.msixPath, ReleaseNotesPath: fixture.notesPath,
		RolloutPercentage: 90, Poll: submission.PollOptions{Interval: 0, Attempts: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "bad payload") {
		t.Fatalf("error = %v", err)
	}
	journal := fixture.server.Journal()
	putIndex := requestIndex(journal, http.MethodPut, "/submissions/created")
	deleteIndex := requestIndex(journal, http.MethodDelete, "/submissions/created")
	verifyIndex := requestIndexAfter(journal, http.MethodGet, "/applications/APP", deleteIndex)
	if putIndex < 0 || deleteIndex <= putIndex || verifyIndex <= deleteIndex {
		t.Fatalf("cleanup sequence missing: %+v", journal)
	}
	assertRequestSequence(t, journal, []string{
		"GET /v1.0/my/applications/APP",
		"GET /v1.0/my/applications/APP/submissions/published/packagerollout",
		"GET /v1.0/my/applications/APP",
		"POST /v1.0/my/applications/APP/submissions",
		"PUT /v1.0/my/applications/APP/submissions/created",
		"DELETE /v1.0/my/applications/APP/submissions/created",
		"GET /v1.0/my/applications/APP",
	})
}

func TestPublishMSIXRefreshesExpiredSASAfter403(t *testing.T) {
	t.Parallel()
	fixture := newPublishFixture(t, publishFixtureOptions{blobForbiddenCount: 1})
	_, err := fixture.publisher.PublishMSIX(context.Background(), submission.PublishMSIXOptions{
		AppID: "APP", MSIXPath: fixture.msixPath, KeepExistingReleaseNotes: true,
		RolloutPercentage: 90, SkipCommit: true, Poll: submission.PollOptions{Interval: 0, Attempts: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := fixture.server.Journal()
	if countJournalRequests(journal, http.MethodPut, "/blob/upload.zip") != 2 || countJournalRequests(journal, http.MethodGet, "/submissions/created") != 1 {
		t.Fatalf("expired SAS recovery sequence = %+v", journal)
	}
	if len(fixture.server.BlobUploads()) != 1 {
		t.Fatalf("successful uploads = %d", len(fixture.server.BlobUploads()))
	}
}

func TestPublishMSIXValidatesIntentBeforeAnyStoreRequest(t *testing.T) {
	t.Parallel()
	fixture := newPublishFixture(t, publishFixtureOptions{})
	for _, options := range []submission.PublishMSIXOptions{
		{AppID: "APP", MSIXPath: fixture.msixPath, RolloutPercentage: 90},
		{AppID: "APP", MSIXPath: fixture.msixPath, ReleaseNotesPath: fixture.notesPath, KeepExistingReleaseNotes: true, RolloutPercentage: 90},
	} {
		if _, err := fixture.publisher.PublishMSIX(context.Background(), options); err == nil {
			t.Fatalf("invalid release-note intent accepted: %+v", options)
		}
	}
	if len(fixture.server.Journal()) != 0 {
		t.Fatalf("preflight contacted Store: %+v", fixture.server.Journal())
	}
}

func TestPublishMSIXAlwaysCleansTemporaryZIPAfterUploadFailure(t *testing.T) {
	t.Parallel()
	fixture := newPublishFixture(t, publishFixtureOptions{uploader: failingUploader{}})
	_, err := fixture.publisher.PublishMSIX(context.Background(), submission.PublishMSIXOptions{
		AppID: "APP", MSIXPath: fixture.msixPath, KeepExistingReleaseNotes: true,
		RolloutPercentage: 90, SkipCommit: true, Poll: submission.PollOptions{Attempts: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "upload failed") {
		t.Fatalf("error = %v", err)
	}
	entries, readErr := os.ReadDir(fixture.uploadTempDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files remain: %+v", entries)
	}
}

type publishFixtureOptions struct {
	rolloutInProgress  bool
	withPending        bool
	blobForbiddenCount int
	uploader           submission.BlobUploader
}

type publishFixture struct {
	server        *fakestore.Server
	publisher     *submission.Publisher
	msixPath      string
	notesPath     string
	uploadTempDir string
}

func newPublishFixture(t *testing.T, options publishFixtureOptions) publishFixture {
	t.Helper()
	dir := t.TempDir()
	msixPath := filepath.Join(dir, "FeedFlow.msix")
	if err := os.WriteFile(msixPath, []byte("fake-msix"), 0o600); err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(dir, "notes.json")
	if err := os.WriteFile(notesPath, []byte(`{"notes":{"en-us":"English note","it":["Prima","Seconda"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rolloutStatus := "PackageRolloutCompleted"
	if options.rolloutInProgress {
		rolloutStatus = "PackageRolloutInProgress"
	}
	app := fakestore.App{ID: "APP", LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published"}}
	submissions := map[string]json.RawMessage{"published": json.RawMessage(`{"id":"published","status":"Published"}`)}
	if options.withPending {
		app.PendingApplicationSubmission = &fakestore.SubmissionRef{ID: "draft"}
		submissions["draft"] = json.RawMessage(`{"id":"draft","status":"PendingCommit"}`)
	}
	created := json.RawMessage(`{
      "applicationCategory":"BooksAndReference",
      "listings":{"en-us":{"baseListing":{"releaseNotes":"old"}},"it":{"baseListing":{"releaseNotes":"old"}}},
      "applicationPackages":[{"fileName":"old.msix","version":"1.0.0.0","fileStatus":"Uploaded"}],
      "packageDeliveryOptions":{"packageRollout":{"isPackageRollout":false},"isMandatoryUpdate":false}
    }`)
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP", App: app, Submissions: submissions,
		Rollouts:           map[string]fakestore.Rollout{"published": {IsPackageRollout: options.rolloutInProgress, PackageRolloutPercentage: 90, PackageRolloutStatus: rolloutStatus}},
		CreateSubmissionID: "created", CreateSubmission: created, CommitStatuses: []string{"PreProcessing"},
		BlobEnabled: true, BlobForbiddenCount: options.blobForbiddenCount,
	})
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
	uploadTempDir := filepath.Join(dir, "uploads")
	if err := os.Mkdir(uploadTempDir, 0o700); err != nil {
		t.Fatal(err)
	}
	uploader := options.uploader
	if uploader == nil {
		uploader = submission.NewAzureBlobUploader()
	}
	publisher, err := submission.NewPublisher(submission.PublisherOptions{
		Client: client, Manager: manager, Uploader: uploader, TempDir: uploadTempDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	return publishFixture{server: server, publisher: publisher, msixPath: msixPath, notesPath: notesPath, uploadTempDir: uploadTempDir}
}

type failingUploader struct{}

func (failingUploader) Upload(context.Context, string, string) error {
	return errors.New("upload failed")
}

func assertRequestSequence(t *testing.T, journal []fakestore.Request, want []string) {
	t.Helper()
	got := make([]string, 0, len(journal))
	for _, request := range journal {
		if strings.HasSuffix(request.Path, "/oauth2/token") {
			continue
		}
		got = append(got, request.Method+" "+request.Path)
	}
	if len(got) != len(want) {
		t.Fatalf("request sequence length = %d, want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("request sequence mismatch at %d\n got: %v\nwant: %v", index, got, want)
		}
	}
}

func findRequest(t *testing.T, journal []fakestore.Request, method, suffix string) fakestore.Request {
	t.Helper()
	for _, request := range journal {
		if request.Method == method && strings.HasSuffix(request.Path, suffix) {
			return request
		}
	}
	t.Fatalf("request %s *%s not found", method, suffix)
	return fakestore.Request{}
}

func requestIndex(journal []fakestore.Request, method, suffix string) int {
	return requestIndexAfter(journal, method, suffix, -1)
}

func requestIndexAfter(journal []fakestore.Request, method, suffix string, after int) int {
	for index := after + 1; index < len(journal); index++ {
		if journal[index].Method == method && strings.HasSuffix(journal[index].Path, suffix) {
			return index
		}
	}
	return -1
}
