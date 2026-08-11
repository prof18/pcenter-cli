package submission

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/prof18/pcenter-cli/internal/metadata"
	"github.com/prof18/pcenter-cli/internal/store"
)

// ListingPublisherOptions supplies the listing-push flow dependencies.
type ListingPublisherOptions struct {
	Client   *store.Client
	Manager  *Manager
	Uploader BlobUploader
	TempDir  string
	Warn     func(string)
}

// ListingPublisher applies repository-backed listing metadata through the submission lifecycle.
type ListingPublisher struct {
	client   *store.Client
	manager  *Manager
	uploader BlobUploader
	tempDir  string
	warn     func(string)
}

// ListingPushOptions is the non-interactive listing mutation contract.
type ListingPushOptions struct {
	AppID              string
	MetadataDir        string
	Directory          metadata.Directory
	ReleaseNotesPath   string
	SkipCommit         bool
	ReplacePending     bool
	AllowLocaleRemoval bool
	Poll               PollOptions
}

// ListingPushResult describes a prepared draft or committed listing submission.
type ListingPushResult struct {
	SubmissionID string            `json:"submissionId"`
	Draft        bool              `json:"draft"`
	NextCommand  string            `json:"nextCommand,omitempty"`
	Plan         metadata.PushPlan `json:"plan"`
	Commit       CommitResult      `json:"commit,omitempty"`
	Warnings     []string          `json:"warnings,omitempty"`
}

// NewListingPublisher validates and constructs a listing publisher.
func NewListingPublisher(options ListingPublisherOptions) (*ListingPublisher, error) {
	if options.Client == nil || options.Manager == nil || options.Uploader == nil {
		return nil, errors.New("client, manager, and uploader are required")
	}
	if options.Warn == nil {
		options.Warn = func(string) {}
	}
	return &ListingPublisher{
		client: options.Client, manager: options.Manager, uploader: options.Uploader,
		tempDir: options.TempDir, warn: options.Warn,
	}, nil
}

// Push executes the listing submission lifecycle and cleans up failures before commit starts.
func (p *ListingPublisher) Push(ctx context.Context, options ListingPushOptions) (result ListingPushResult, returnErr error) {
	if strings.TrimSpace(options.AppID) == "" {
		return result, errors.New("app id is required")
	}
	if options.Directory.Marker.AppID != options.AppID {
		return result, fmt.Errorf("metadata directory belongs to app %s, not %s", options.Directory.Marker.AppID, options.AppID)
	}
	if err := metadata.ValidateListings(options.Directory.Listings); err != nil {
		return result, err
	}
	if err := metadata.ValidateImages(options.MetadataDir, options.Directory.Images); err != nil {
		return result, err
	}
	var notesData []byte
	var err error
	if options.ReleaseNotesPath != "" {
		notesData, err = os.ReadFile(options.ReleaseNotesPath)
		if err != nil {
			return result, fmt.Errorf("read release notes: %w", err)
		}
	}

	app, err := p.client.Application(ctx, options.AppID)
	if err != nil {
		return result, err
	}
	if published := app.LastPublishedApplicationSubmission; published != nil {
		publishedSubmission, getErr := p.client.Submission(ctx, options.AppID, published.ID)
		if getErr != nil {
			return result, getErr
		}
		preflight, preflightErr := metadata.BuildPushPlan(options.MetadataDir, options.Directory, publishedSubmission.Raw, options.AllowLocaleRemoval)
		if preflightErr != nil {
			return result, preflightErr
		}
		if options.ReleaseNotesPath != "" {
			if _, _, preflightErr = ApplyReleaseNotes(preflight.Body, notesData, options.ReleaseNotesPath); preflightErr != nil {
				return result, preflightErr
			}
		}
		rollout, rolloutErr := p.client.Rollout(ctx, options.AppID, published.ID)
		if rolloutErr != nil {
			return result, rolloutErr
		}
		if rollout.IsPackageRollout && rollout.PackageRolloutStatus == "PackageRolloutInProgress" {
			if _, finalizeErr := p.manager.FinalizeRollout(ctx, options.AppID, published.ID); finalizeErr != nil {
				return result, finalizeErr
			}
		}
	}

	app, err = p.client.Application(ctx, options.AppID)
	if err != nil {
		return result, err
	}
	if pending := app.PendingApplicationSubmission; pending != nil {
		pendingSubmission, pendingErr := p.client.Submission(ctx, options.AppID, pending.ID)
		if pendingErr != nil {
			return result, pendingErr
		}
		if !options.ReplacePending {
			return result, fmt.Errorf("app already has pending submission %s with status %s; resolve it or pass --replace-pending", pending.ID, pendingSubmission.Status)
		}
		if !IsRemovableDraftStatus(pendingSubmission.Status) {
			return result, fmt.Errorf("pending submission %s has status %s; only an uncommitted or failed submission can be replaced automatically", pending.ID, pendingSubmission.Status)
		}
		if err := p.manager.DeleteDraft(ctx, options.AppID, pending.ID); err != nil {
			return result, err
		}
	}

	created, err := p.manager.Create(ctx, options.AppID)
	if err != nil {
		return result, err
	}
	result.SubmissionID = created.ID
	commitAccepted := false
	defer func() {
		if returnErr == nil || result.SubmissionID == "" || commitAccepted {
			return
		}
		if cleanupErr := p.manager.DeleteDraft(ctx, options.AppID, result.SubmissionID); cleanupErr != nil {
			warning := fmt.Sprintf("could not delete uncommitted Store submission %s after failure: %v", result.SubmissionID, cleanupErr)
			result.Warnings = append(result.Warnings, warning)
			p.warn(warning)
		}
	}()

	result.Plan, err = metadata.BuildPushPlan(options.MetadataDir, options.Directory, created.Raw, options.AllowLocaleRemoval)
	if err != nil {
		return result, err
	}
	if options.ReleaseNotesPath != "" {
		result.Plan.Body, result.Warnings, err = ApplyReleaseNotes(result.Plan.Body, notesData, options.ReleaseNotesPath)
		if err != nil {
			return result, err
		}
		for _, warning := range result.Warnings {
			p.warn(warning)
		}
	}
	putPath := submissionPath(options.AppID, result.SubmissionID)
	updatedRaw, err := p.client.DoJSON(ctx, http.MethodPut, putPath, result.Plan.Body)
	if err != nil {
		return result, err
	}
	updated, err := decodeSubmission(updatedRaw)
	if err != nil {
		return result, err
	}

	if len(result.Plan.Uploads) > 0 {
		if updated.FileUploadURL == "" {
			return result, errors.New("updated submission did not contain fileUploadUrl")
		}
		entries := make([]ArchiveEntry, len(result.Plan.Uploads))
		for index, upload := range result.Plan.Uploads {
			entries[index] = ArchiveEntry{SourcePath: upload.SourcePath, Name: upload.Name}
		}
		if p.tempDir != "" {
			if err := os.MkdirAll(p.tempDir, 0o700); err != nil {
				return result, fmt.Errorf("create upload temporary directory: %w", err)
			}
		}
		tempFile, err := os.CreateTemp(p.tempDir, "pcenter-listing-upload-*.zip")
		if err != nil {
			return result, fmt.Errorf("create temporary upload ZIP: %w", err)
		}
		zipPath := tempFile.Name()
		if err := tempFile.Close(); err != nil {
			_ = os.Remove(zipPath)
			return result, fmt.Errorf("close temporary upload ZIP: %w", err)
		}
		defer func() {
			if removeErr := os.Remove(zipPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				warning := "could not remove temporary listing upload ZIP"
				result.Warnings = append(result.Warnings, warning)
				p.warn(warning)
			}
		}()
		if err := CreateUploadZIP(zipPath, entries); err != nil {
			return result, err
		}
		if err := uploadWithSASRefresh(ctx, p.client, p.uploader, options.AppID, result.SubmissionID, updated.FileUploadURL, zipPath); err != nil {
			return result, err
		}
	}

	if options.SkipCommit {
		result.Draft = true
		result.NextCommand = "pcenter submission commit"
		return result, nil
	}
	result.Commit, err = p.manager.Commit(ctx, options.AppID, result.SubmissionID, options.Poll)
	commitAccepted = result.Commit.Accepted
	if err != nil {
		return result, err
	}
	return result, nil
}

func uploadWithSASRefresh(ctx context.Context, client *store.Client, uploader BlobUploader, appID, submissionID, uploadURL, zipPath string) error {
	currentURL := uploadURL
	for attempt := 1; attempt <= 4; attempt++ {
		err := uploader.Upload(ctx, currentURL, zipPath)
		if err == nil {
			return nil
		}
		if !IsForbiddenUploadError(err) || attempt == 4 {
			return err
		}
		fresh, refreshErr := client.Submission(ctx, appID, submissionID)
		if refreshErr != nil {
			return fmt.Errorf("blob upload SAS expired and submission refresh failed: %w", refreshErr)
		}
		if fresh.FileUploadURL == "" {
			return errors.New("refreshed submission did not contain fileUploadUrl")
		}
		currentURL = fresh.FileUploadURL
	}
	return errors.New("blob upload attempts exhausted")
}
