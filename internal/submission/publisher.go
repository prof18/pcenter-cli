package submission

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/prof18/pcenter-cli/internal/store"
)

// PublisherOptions supplies the publish flow's tested boundaries.
type PublisherOptions struct {
	Client   *store.Client
	Manager  *Manager
	Uploader BlobUploader
	TempDir  string
	Warn     func(string)
}

// Publisher prepares, uploads, and optionally commits MSIX submissions.
type Publisher struct {
	client   *store.Client
	manager  *Manager
	uploader BlobUploader
	tempDir  string
	warn     func(string)
}

// PublishMSIXOptions is the non-interactive publish contract.
type PublishMSIXOptions struct {
	AppID                    string
	MSIXPath                 string
	ReleaseNotesPath         string
	KeepExistingReleaseNotes bool
	RolloutPercentage        float64
	SkipCommit               bool
	ReplacePending           bool
	Poll                     PollOptions
}

// PublishResult describes a prepared draft or committed submission.
type PublishResult struct {
	SubmissionID      string       `json:"submissionId"`
	PackageFileName   string       `json:"packageFileName"`
	RolloutPercentage float64      `json:"rolloutPercentage"`
	Draft             bool         `json:"draft"`
	NextCommand       string       `json:"nextCommand,omitempty"`
	Commit            CommitResult `json:"commit,omitempty"`
	Warnings          []string     `json:"warnings,omitempty"`
}

// NewPublisher validates and constructs a publisher.
func NewPublisher(options PublisherOptions) (*Publisher, error) {
	if options.Client == nil || options.Manager == nil || options.Uploader == nil {
		return nil, errors.New("client, manager, and uploader are required")
	}
	if options.Warn == nil {
		options.Warn = func(string) {}
	}
	return &Publisher{
		client: options.Client, manager: options.Manager, uploader: options.Uploader,
		tempDir: options.TempDir, warn: options.Warn,
	}, nil
}

// PublishMSIX executes the locked publish sequence, cleaning up uncommitted drafts on failure.
func (p *Publisher) PublishMSIX(ctx context.Context, options PublishMSIXOptions) (result PublishResult, returnErr error) {
	packageFileName, notesData, err := validatePublishOptions(options)
	if err != nil {
		return PublishResult{}, err
	}
	result.PackageFileName = packageFileName
	result.RolloutPercentage = options.RolloutPercentage

	app, err := p.client.Application(ctx, options.AppID)
	if err != nil {
		return result, err
	}
	if published := app.LastPublishedApplicationSubmission; published != nil {
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
		if pendingSubmission.Status != "PendingCommit" {
			return result, fmt.Errorf("pending submission %s has status %s; only PendingCommit can be replaced automatically", pending.ID, pendingSubmission.Status)
		}
		if deleteErr := p.manager.DeleteDraft(ctx, options.AppID, pending.ID); deleteErr != nil {
			return result, deleteErr
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

	workingSubmission := created.Raw
	if options.ReleaseNotesPath != "" {
		workingSubmission, result.Warnings, err = ApplyReleaseNotes(workingSubmission, notesData, options.ReleaseNotesPath)
		if err != nil {
			return result, err
		}
		for _, warning := range result.Warnings {
			p.warn(warning)
		}
	}
	putBody, err := BuildPublishBody(workingSubmission, packageFileName, options.RolloutPercentage)
	if err != nil {
		return result, err
	}
	putPath := submissionPath(options.AppID, result.SubmissionID)
	updatedRaw, err := p.client.DoJSON(ctx, http.MethodPut, putPath, putBody)
	if err != nil {
		return result, err
	}
	updated, err := decodeSubmission(updatedRaw)
	if err != nil {
		return result, err
	}
	if updated.FileUploadURL == "" {
		return result, errors.New("updated submission did not contain fileUploadUrl")
	}

	tempFile, err := os.CreateTemp(p.tempDir, "pcenter-upload-*.zip")
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
			warning := "could not remove temporary upload ZIP"
			result.Warnings = append(result.Warnings, warning)
			p.warn(warning)
		}
	}()
	if err := CreateUploadZIP(zipPath, []ArchiveEntry{{SourcePath: options.MSIXPath, Name: packageFileName}}); err != nil {
		return result, err
	}
	if err := p.uploadWithSASRefresh(ctx, options.AppID, result.SubmissionID, updated.FileUploadURL, zipPath); err != nil {
		return result, err
	}

	if options.SkipCommit {
		result.Draft = true
		result.NextCommand = "pcenter submission commit"
		return result, nil
	}
	commit, err := p.manager.Commit(ctx, options.AppID, result.SubmissionID, options.Poll)
	result.Commit = commit
	commitAccepted = commit.Accepted
	if err != nil {
		return result, err
	}
	return result, nil
}

func (p *Publisher) uploadWithSASRefresh(ctx context.Context, appID, submissionID, uploadURL, zipPath string) error {
	return uploadWithSASRefresh(ctx, p.client, p.uploader, appID, submissionID, uploadURL, zipPath)
}

func validatePublishOptions(options PublishMSIXOptions) (string, []byte, error) {
	if strings.TrimSpace(options.AppID) == "" {
		return "", nil, errors.New("app id is required")
	}
	if options.RolloutPercentage <= 0 || options.RolloutPercentage > 100 {
		return "", nil, errors.New("rollout percentage must be greater than 0 and at most 100")
	}
	if (options.ReleaseNotesPath == "") == !options.KeepExistingReleaseNotes {
		return "", nil, errors.New("exactly one of release notes or keep-existing-release-notes is required")
	}
	info, err := os.Stat(options.MSIXPath)
	if err != nil {
		return "", nil, fmt.Errorf("open MSIX package: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, errors.New("MSIX path must be a regular file")
	}
	packageFileName := filepath.Base(options.MSIXPath)
	if packageFileName == "." || packageFileName == string(filepath.Separator) || packageFileName == "" {
		return "", nil, errors.New("MSIX path does not have a valid file name")
	}
	if options.ReleaseNotesPath == "" {
		return packageFileName, nil, nil
	}
	notesData, err := os.ReadFile(options.ReleaseNotesPath)
	if err != nil {
		return "", nil, fmt.Errorf("read release notes: %w", err)
	}
	if _, err := parseReleaseNotes(notesData); err != nil {
		return "", nil, fmt.Errorf("release notes file %q: %w", options.ReleaseNotesPath, err)
	}
	return packageFileName, notesData, nil
}
