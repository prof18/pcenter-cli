// Package submission implements verify-between-attempts Partner Center mutation flows.
package submission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/prof18/pcenter-cli/internal/store"
	storetypes "github.com/prof18/pcenter-cli/internal/store/types"
)

var (
	finalizeBackoff = store.Backoff{Base: 20 * time.Second, Cap: 240 * time.Second, Attempts: 5}
	createBackoff   = store.Backoff{Base: 20 * time.Second, Cap: 120 * time.Second, Attempts: 4}
	commitBackoff   = store.Backoff{Base: 15 * time.Second, Cap: 120 * time.Second, Attempts: 4}
	deleteBackoff   = store.Backoff{Base: 10 * time.Second, Cap: 60 * time.Second, Attempts: 3}
)

// ManagerOptions defines the flow dependencies.
type ManagerOptions struct {
	Client *store.Client
	Clock  store.Clock
	Rand   store.Rand
}

// Manager drives endpoint-specific verify loops.
type Manager struct {
	client *store.Client
	clock  store.Clock
	rand   store.Rand
}

// PollOptions controls status polling. Tests inject a clock, so no real sleep is required.
type PollOptions struct {
	Interval time.Duration
	Attempts int
}

// CommitResult describes the point reached by the post-commit startup poll.
type CommitResult struct {
	Status        string          `json:"status"`
	StatusDetails json.RawMessage `json:"statusDetails,omitempty"`
	Warning       string          `json:"warning,omitempty"`
	Accepted      bool            `json:"accepted"`
}

// StatusClass is the full-status taxonomy used by submission watch.
type StatusClass string

const (
	// StatusFailed is a terminal failure.
	StatusFailed StatusClass = "failed"
	// StatusSuccess is a terminal successful publication.
	StatusSuccess StatusClass = "success"
	// StatusNeutral is a terminal cancellation.
	StatusNeutral StatusClass = "neutral"
	// StatusInProgress requires further polling.
	StatusInProgress StatusClass = "in-progress"
)

// WatchResult is the latest observed submission status and its classification.
type WatchResult struct {
	Status         string          `json:"status"`
	StatusDetails  json.RawMessage `json:"statusDetails,omitempty"`
	Classification StatusClass     `json:"classification"`
	// Warning is set when the poll budget ran out with the submission still in
	// progress. That is not a failure — it is this command giving up watching,
	// not the Store giving up working — so it is reported the way `commit`
	// reports the same situation rather than as an error.
	Warning string `json:"warning,omitempty"`
}

// NewManager validates and constructs a flow manager.
func NewManager(options ManagerOptions) (*Manager, error) {
	if options.Client == nil || options.Clock == nil || options.Rand == nil {
		return nil, errors.New("client, clock, and rand are required")
	}
	return &Manager{client: options.Client, clock: options.Clock, rand: options.Rand}, nil
}

// FinalizeRollout verifies rollout state after every failed POST before retrying.
func (m *Manager) FinalizeRollout(ctx context.Context, appID, submissionID string) (storetypes.Rollout, error) {
	path := submissionPath(appID, submissionID) + "/finalizepackagerollout"
	var lastError error
	for attempt := 1; attempt <= finalizeBackoff.Attempts; attempt++ {
		raw, err := m.client.DoJSON(ctx, http.MethodPost, path, nil)
		if err == nil {
			var rollout storetypes.Rollout
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &rollout)
			}
			return rollout, nil
		}
		if isConflict(err) {
			return storetypes.Rollout{}, m.rolloutStateError(ctx, appID, submissionID, err)
		}
		lastError = err
		if err := m.clock.Sleep(ctx, m.retryDelay(finalizeBackoff, attempt, err)); err != nil {
			return storetypes.Rollout{}, err
		}
		rollout, verifyErr := m.client.Rollout(ctx, appID, submissionID)
		if verifyErr != nil {
			lastError = fmt.Errorf("finalize and verification failed: %w", errors.Join(lastError, verifyErr))
			continue
		}
		if rollout.PackageRolloutStatus != "PackageRolloutInProgress" {
			return rollout, nil
		}
	}
	return storetypes.Rollout{}, fmt.Errorf("finalize rollout did not change state after %d attempts: %w", finalizeBackoff.Attempts, lastError)
}

// Create creates a draft, adopting a pending draft that materialized behind an error response.
func (m *Manager) Create(ctx context.Context, appID string) (storetypes.Submission, error) {
	path := "/applications/" + url.PathEscape(appID) + "/submissions"
	var lastError error
	for attempt := 1; attempt <= createBackoff.Attempts; attempt++ {
		raw, err := m.client.DoJSON(ctx, http.MethodPost, path, nil)
		if err == nil {
			return decodeSubmission(raw)
		}
		lastError = err
		if err := m.clock.Sleep(ctx, m.retryDelay(createBackoff, attempt, err)); err != nil {
			return storetypes.Submission{}, err
		}
		app, verifyErr := m.client.Application(ctx, appID)
		if verifyErr != nil {
			lastError = fmt.Errorf("create and verification failed: %w", errors.Join(lastError, verifyErr))
			continue
		}
		if app.PendingApplicationSubmission != nil {
			return m.client.Submission(ctx, appID, app.PendingApplicationSubmission.ID)
		}
	}
	return storetypes.Submission{}, fmt.Errorf("create submission did not materialize after %d attempts: %w", createBackoff.Attempts, lastError)
}

// DeleteDraft verifies the application after every DELETE, including successful responses.
func (m *Manager) DeleteDraft(ctx context.Context, appID, submissionID string) error {
	path := submissionPath(appID, submissionID)
	var lastError error
	for attempt := 1; attempt <= deleteBackoff.Attempts; attempt++ {
		_, requestErr := m.client.DoJSON(ctx, http.MethodDelete, path, nil)
		lastError = requestErr
		app, verifyErr := m.client.Application(ctx, appID)
		if verifyErr == nil && (app.PendingApplicationSubmission == nil || app.PendingApplicationSubmission.ID != submissionID) {
			return nil
		}
		if verifyErr != nil {
			lastError = fmt.Errorf("delete and verification failed: %w", errors.Join(requestErr, verifyErr))
		} else if requestErr == nil {
			lastError = errors.New("delete returned success but the draft is still pending")
		}
		if attempt < deleteBackoff.Attempts {
			if err := m.clock.Sleep(ctx, m.retryDelay(deleteBackoff, attempt, requestErr)); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("delete draft did not remove pending submission after %d attempts: %w", deleteBackoff.Attempts, lastError)
}

// Commit uses the commit verification loop and then polls until commit startup is complete.
func (m *Manager) Commit(ctx context.Context, appID, submissionID string, poll PollOptions) (CommitResult, error) {
	if err := validatePollOptions(poll); err != nil {
		return CommitResult{}, err
	}
	path := submissionPath(appID, submissionID) + "/commit"
	accepted := false
	var lastError error
	for attempt := 1; attempt <= commitBackoff.Attempts; attempt++ {
		_, err := m.client.DoJSON(ctx, http.MethodPost, path, nil)
		if err == nil {
			accepted = true
			break
		}
		lastError = err
		if err := m.clock.Sleep(ctx, m.retryDelay(commitBackoff, attempt, err)); err != nil {
			return CommitResult{}, err
		}
		status, verifyErr := m.client.SubmissionStatus(ctx, appID, submissionID)
		if verifyErr != nil {
			lastError = fmt.Errorf("commit and verification failed: %w", errors.Join(lastError, verifyErr))
			continue
		}
		if status.Status != "PendingCommit" {
			accepted = true
			break
		}
	}
	if !accepted {
		return CommitResult{}, fmt.Errorf("commit was not accepted after %d attempts: %w", commitBackoff.Attempts, lastError)
	}
	result, err := m.pollCommitStartup(ctx, appID, submissionID, poll)
	result.Accepted = true
	return result, err
}

// Watch polls using the complete submission-status taxonomy.
func (m *Manager) Watch(ctx context.Context, appID, submissionID string, poll PollOptions) (WatchResult, error) {
	if err := validatePollOptions(poll); err != nil {
		return WatchResult{}, err
	}
	var latest WatchResult
	for attempt := 1; attempt <= poll.Attempts; attempt++ {
		status, err := m.client.SubmissionStatus(ctx, appID, submissionID)
		if err != nil {
			return WatchResult{}, err
		}
		latest = WatchResult{Status: status.Status, StatusDetails: status.StatusDetails, Classification: ClassifyStatus(status.Status)}
		switch latest.Classification {
		case StatusFailed:
			return latest, statusFailure(status)
		case StatusSuccess, StatusNeutral:
			return latest, nil
		case StatusInProgress:
			if attempt < poll.Attempts {
				if err := m.clock.Sleep(ctx, poll.Interval); err != nil {
					return WatchResult{}, err
				}
			}
		}
	}
	// Running out of attempts means this command stopped watching, not that the
	// submission is in trouble: certification legitimately takes hours. Failing
	// here turns a healthy release into a red CI job, and makes the documented
	// in-progress classification unreachable. Report it and exit clean.
	latest.Warning = fmt.Sprintf(
		"submission is still %s after %d poll attempts; Partner Center continues processing", latest.Status, poll.Attempts)
	return latest, nil
}

// SetRolloutPercentage updates the rollout percentage query parameter and confirms state.
func (m *Manager) SetRolloutPercentage(ctx context.Context, appID, submissionID string, percentage float64) (storetypes.Rollout, error) {
	query := "?percentage=" + url.QueryEscape(strconv.FormatFloat(percentage, 'f', -1, 64))
	return m.rolloutMutation(ctx, appID, submissionID, "/updatepackagerolloutpercentage"+query)
}

// HaltRollout halts a package rollout and confirms the stopped state.
func (m *Manager) HaltRollout(ctx context.Context, appID, submissionID string) (storetypes.Rollout, error) {
	return m.rolloutMutation(ctx, appID, submissionID, "/haltpackagerollout")
}

func (m *Manager) rolloutMutation(ctx context.Context, appID, submissionID, suffix string) (storetypes.Rollout, error) {
	_, err := m.client.DoJSON(ctx, http.MethodPost, submissionPath(appID, submissionID)+suffix, nil)
	if err != nil {
		if isConflict(err) {
			return storetypes.Rollout{}, m.rolloutStateError(ctx, appID, submissionID, err)
		}
		return storetypes.Rollout{}, err
	}
	return m.client.Rollout(ctx, appID, submissionID)
}

func (m *Manager) rolloutStateError(ctx context.Context, appID, submissionID string, operationError error) error {
	rollout, verifyErr := m.client.Rollout(ctx, appID, submissionID)
	if verifyErr != nil {
		return fmt.Errorf("rollout operation is invalid for the current state (HTTP 409), and state lookup failed: %w", verifyErr)
	}
	return fmt.Errorf("rollout operation is invalid for current state %s at %s%% (HTTP 409): %w",
		rollout.PackageRolloutStatus, strconv.FormatFloat(rollout.PackageRolloutPercentage, 'f', -1, 64), operationError)
}

func (m *Manager) retryDelay(policy store.Backoff, attempt int, operationError error) time.Duration {
	delay := policy.Delay(attempt, m.rand)
	var apiError *store.APIError
	if errors.As(operationError, &apiError) && (apiError.StatusCode == http.StatusTooManyRequests || apiError.StatusCode == http.StatusServiceUnavailable) {
		if retryAfter, ok := policy.RetryAfter(apiError.RetryAfter, m.clock.Now()); ok {
			return retryAfter
		}
	}
	return delay
}

func (m *Manager) pollCommitStartup(ctx context.Context, appID, submissionID string, poll PollOptions) (CommitResult, error) {
	var latest storetypes.SubmissionStatus
	for attempt := 1; attempt <= poll.Attempts; attempt++ {
		status, err := m.client.SubmissionStatus(ctx, appID, submissionID)
		if err != nil {
			return CommitResult{}, err
		}
		latest = status
		if status.Status == "CommitFailed" || status.Status == "PreProcessingFailed" {
			return CommitResult{}, statusFailure(status)
		}
		if status.Status != "PendingCommit" && status.Status != "CommitStarted" {
			return CommitResult{Status: status.Status, StatusDetails: status.StatusDetails}, nil
		}
		if attempt < poll.Attempts {
			if err := m.clock.Sleep(ctx, poll.Interval); err != nil {
				return CommitResult{}, err
			}
		}
	}
	warning := fmt.Sprintf("submission is still %s after %d poll attempts; Partner Center continues processing", latest.Status, poll.Attempts)
	return CommitResult{Status: latest.Status, StatusDetails: latest.StatusDetails, Warning: warning}, nil
}

// ClassifyStatus maps all documented Partner Center statuses to watch behavior.
func ClassifyStatus(status string) StatusClass {
	switch status {
	case "CommitFailed", "PreProcessingFailed", "CertificationFailed", "PublishFailed", "ReleaseFailed":
		return StatusFailed
	case "Published":
		return StatusSuccess
	case "Canceled":
		return StatusNeutral
	default:
		return StatusInProgress
	}
}

// IsRemovableDraftStatus reports whether a pending submission can be deleted to
// unblock the app.
//
// PendingCommit is the never-committed draft. The failed statuses matter just as
// much: a submission that failed commit, pre-processing, certification, publish
// or release is finished, not in flight — but it is still the app's one pending
// submission, so until it is removed nothing else can be created. Refusing to
// delete it leaves the app wedged with no way out of the CLI, which is exactly
// the situation this tool exists to get you out of.
//
// Anything else is genuinely in progress and must not be deleted from under the
// Store.
func IsRemovableDraftStatus(status string) bool {
	return status == "PendingCommit" || ClassifyStatus(status) == StatusFailed
}

func statusFailure(status storetypes.SubmissionStatus) error {
	details := strings.TrimSpace(string(status.StatusDetails))
	if details == "" {
		details = "{}"
	}
	return fmt.Errorf("submission entered failed status %s: %s", status.Status, details)
}

func validatePollOptions(poll PollOptions) error {
	if poll.Attempts < 1 {
		return errors.New("poll attempts must be positive")
	}
	if poll.Interval < 0 {
		return errors.New("poll interval must be non-negative")
	}
	return nil
}

func decodeSubmission(raw json.RawMessage) (storetypes.Submission, error) {
	var result storetypes.Submission
	if err := json.Unmarshal(raw, &result); err != nil {
		return storetypes.Submission{}, fmt.Errorf("decode created submission: %w", err)
	}
	result.Raw = append(json.RawMessage(nil), raw...)
	return result, nil
}

func submissionPath(appID, submissionID string) string {
	return "/applications/" + url.PathEscape(appID) + "/submissions/" + url.PathEscape(submissionID)
}

func isConflict(err error) bool {
	var apiError *store.APIError
	return errors.As(err, &apiError) && apiError.StatusCode == http.StatusConflict
}
