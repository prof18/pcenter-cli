// Package cli defines the pcenter Cobra command tree.
package cli

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/prof18/pcenter-cli/internal/config"
	metadataflow "github.com/prof18/pcenter-cli/internal/metadata"
	"github.com/prof18/pcenter-cli/internal/output"
	"github.com/prof18/pcenter-cli/internal/store"
	storetypes "github.com/prof18/pcenter-cli/internal/store/types"
	submissionflow "github.com/prof18/pcenter-cli/internal/submission"
)

// BuildInfo is injected through linker flags in release builds.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"buildDate"`
}

// Dependencies are process boundaries overridden by tests.
type Dependencies struct {
	Stdout      io.Writer
	Stderr      io.Writer
	Environment config.Environment
	IsTTY       bool
	Now         func() time.Time
	Build       BuildInfo
	HTTPClient  *http.Client
	Clock       store.Clock
	Rand        store.Rand
	// PromptLine and PromptSecret are only ever called by `auth login`, and
	// only when IsTTY. Injected so the prompting path is testable without a pty.
	PromptLine   func(prompt string) (string, error)
	PromptSecret func(prompt string) (string, error)
}

type commandState struct {
	dependencies Dependencies
	outputFlag   string
	envFile      string
	appID        string
	verbose      bool
	format       output.Format
	client       *store.Client
	manager      *submissionflow.Manager
	publisher    *submissionflow.Publisher
	listingPush  *submissionflow.ListingPublisher
	config       config.Config
	// warnings accumulate so JSON output can carry them in the payload. Echoing
	// them to stderr as prose too would make an agent reading both count each
	// one twice, and one reading only stdout miss them entirely.
	warnings []string
}

// warn records a warning, printing it immediately only for human output.
func (s *commandState) warn(message string) {
	s.warnings = append(s.warnings, message)
	if s.format != output.JSON {
		_, _ = fmt.Fprintln(s.dependencies.Stderr, "Warning:", message)
	}
}

type usageError struct{ error }
type failureError struct{ error }

// warningsError carries warnings collected before a failure into the error
// payload, so JSON callers see them without a second output channel.
type warningsError struct {
	error
	warnings []string
}

func (e *warningsError) Unwrap() error { return e.error }

func (e *warningsError) ErrorDetails() map[string]any {
	return map[string]any{"warnings": e.warnings}
}

// Execute runs pcenter and returns a process exit code.
func Execute(ctx context.Context, args []string, dependencies Dependencies) int {
	if dependencies.Stdout == nil {
		dependencies.Stdout = io.Discard
	}
	if dependencies.Stderr == nil {
		dependencies.Stderr = io.Discard
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.HTTPClient == nil {
		dependencies.HTTPClient = http.DefaultClient
	}
	if dependencies.Clock == nil {
		dependencies.Clock = commandClock{}
	}
	if dependencies.Rand == nil {
		dependencies.Rand = commandRand{}
	}
	if dependencies.PromptLine == nil {
		dependencies.PromptLine = promptLine
	}
	if dependencies.PromptSecret == nil {
		dependencies.PromptSecret = promptSecret
	}
	state := &commandState{dependencies: dependencies}
	root := state.rootCommand()
	root.SetArgs(args)
	root.SetOut(dependencies.Stdout)
	root.SetErr(dependencies.Stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		format, formatErr := output.ResolveFormat(state.outputFlag, dependencies.IsTTY)
		if formatErr != nil {
			format = output.JSON
		}
		inner := unwrapCommandError(err)
		// On a failure path no result is rendered, so warnings collected during
		// the attempt would otherwise be lost in JSON mode. Cleanup warnings —
		// "could not delete the draft I just created" — are exactly the ones a
		// caller must not miss.
		if len(state.warnings) > 0 {
			inner = &warningsError{error: inner, warnings: state.warnings}
		}
		output.WriteError(dependencies.Stderr, format, inner)
		// A code carried by the underlying error decides the exit status, so
		// automation can branch on $? alone: 3 auth, 4 permanent state
		// conflict, 5 throttled. The usage/failure wrapper is the fallback.
		if code := output.CodeOf(inner); code != "" {
			return output.ExitCodeFor(code)
		}
		var failed failureError
		if errors.As(err, &failed) {
			return output.ExitFailure
		}
		return output.ExitUsage
	}
	return output.ExitSuccess
}

func (s *commandState) rootCommand() *cobra.Command {
	build := normalizeBuildInfo(s.dependencies.Build)
	root := &cobra.Command{
		Use:           "pcenter",
		Short:         "Microsoft Partner Center CLI",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", build.Version, build.Commit, build.Date),
	}
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.PersistentFlags().StringVar(&s.outputFlag, "output", "", "output format: json or table")
	root.PersistentFlags().StringVar(&s.envFile, "env-file", "", "credential env file")
	root.PersistentFlags().StringVar(&s.appID, "app-id", "", "Microsoft Store product id")
	root.PersistentFlags().BoolVar(&s.verbose, "verbose", false, "log redacted HTTP requests to stderr")

	root.AddCommand(
		s.versionCommand(build),
		s.authCommand(),
		s.appCommand(),
		s.localesCommand(),
		s.reviewsCommand(),
		s.submissionCommand(),
		s.rolloutCommand(),
		s.publishCommand(),
		s.listingCommand(),
	)
	return root
}

func (s *commandState) listingCommand() *cobra.Command {
	parent := &cobra.Command{Use: "listing", Short: "Manage Store listing metadata"}
	parent.AddCommand(s.listingShowCommand(), s.listingPullCommand(), s.listingPushCommand())
	return parent
}

func (s *commandState) listingPullCommand() *cobra.Command {
	var dir string
	var published, pending bool
	command := &cobra.Command{
		Use:   "pull",
		Short: "Snapshot Store listing metadata into a directory",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(dir) == "" {
				return usageError{errors.New("listing pull requires --dir")}
			}
			if err := s.prepareOutput(); err != nil {
				return err
			}
			if err := s.prepareClient(); err != nil {
				return err
			}
			app, err := s.client.Application(cmd.Context(), s.config.AppID)
			if err != nil {
				return failureError{err}
			}
			var reference *storetypes.SubmissionReference
			source := "published"
			if pending {
				reference = app.PendingApplicationSubmission
				source = "pending"
			} else {
				reference = app.LastPublishedApplicationSubmission
			}
			if reference == nil {
				return failureError{fmt.Errorf("application has no %s submission", source)}
			}
			submission, err := s.client.Submission(cmd.Context(), s.config.AppID, reference.ID)
			if err != nil {
				return failureError{err}
			}
			build := normalizeBuildInfo(s.dependencies.Build)
			snapshot, _, err := metadataflow.SnapshotFromSubmission(dir, s.config.AppID, "pcenter "+build.Version, s.dependencies.Now(), submission.Raw)
			if err != nil {
				return failureError{err}
			}
			if err := metadataflow.WriteSnapshot(dir, snapshot); err != nil {
				return failureError{err}
			}
			remoteOnly := 0
			matchedLocal := 0
			for _, entries := range snapshot.Images.Images {
				for _, entry := range entries {
					if entry.RemoteOnly {
						remoteOnly++
					} else {
						matchedLocal++
					}
				}
			}
			result := struct {
				Directory          string `json:"directory"`
				Source             string `json:"source"`
				SubmissionID       string `json:"submissionId"`
				ListingCount       int    `json:"listingCount"`
				RemoteOnlyImages   int    `json:"remoteOnlyImages"`
				MatchedLocalImages int    `json:"matchedLocalImages"`
			}{dir, source, reference.ID, len(snapshot.Listings), remoteOnly, matchedLocal}
			renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
			if s.format == output.JSON {
				return wrapFailure(renderer.Value(result))
			}
			return wrapFailure(renderer.Rows(
				[]string{"DIRECTORY", "SOURCE", "SUBMISSION", "LISTINGS", "REMOTE-ONLY IMAGES", "MATCHED LOCAL IMAGES"},
				[][]string{{result.Directory, result.Source, result.SubmissionID, strconv.Itoa(result.ListingCount), strconv.Itoa(result.RemoteOnlyImages), strconv.Itoa(result.MatchedLocalImages)}},
			))
		},
	}
	command.Flags().StringVar(&dir, "dir", "", "metadata directory")
	command.Flags().BoolVar(&published, "published", false, "pull the last published submission (default)")
	command.Flags().BoolVar(&pending, "pending", false, "pull the pending submission")
	command.MarkFlagsMutuallyExclusive("published", "pending")
	return command
}

func (s *commandState) listingPushCommand() *cobra.Command {
	var dir, releaseNotesPath string
	var dryRun, skipCommit, yes, replacePending, allowLocaleRemoval bool
	command := &cobra.Command{
		Use:   "push",
		Short: "Apply a metadata directory to a Store submission",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(dir) == "" {
				return usageError{errors.New("listing push requires --dir")}
			}
			modeCount := 0
			for _, selected := range []bool{dryRun, skipCommit, yes} {
				if selected {
					modeCount++
				}
			}
			if modeCount != 1 {
				return usageError{errors.New("exactly one of --dry-run, --skip-commit, or --yes is required")}
			}
			if err := s.prepareClient(); err != nil {
				return err
			}
			directory, err := metadataflow.LoadDirectory(dir, s.config.AppID)
			if err != nil {
				return failureError{err}
			}
			if !dryRun {
				if err := s.prepareListingPublisher(); err != nil {
					return err
				}
				poll, err := pollOptions(30, 20)
				if err != nil {
					return usageError{err}
				}
				result, err := s.listingPush.Push(cmd.Context(), submissionflow.ListingPushOptions{
					AppID: s.config.AppID, MetadataDir: dir, Directory: directory,
					ReleaseNotesPath: releaseNotesPath, SkipCommit: skipCommit, ReplacePending: replacePending,
					AllowLocaleRemoval: allowLocaleRemoval, Poll: poll,
				})
				if err != nil {
					return failureError{err}
				}
				renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
				if s.format == output.JSON {
					return wrapFailure(renderer.Value(result))
				}
				status := result.Commit.Status
				if result.Draft {
					status = "PendingCommit draft"
				}
				if err := renderer.Rows(
					[]string{"SUBMISSION", "STATUS", "LISTING CHANGES", "IMAGE CHANGES", "UPLOADS"},
					[][]string{{result.SubmissionID, status, strconv.Itoa(len(result.Plan.ListingChanges)), strconv.Itoa(len(result.Plan.ImageChanges)), strconv.Itoa(len(result.Plan.Uploads))}},
				); err != nil {
					return failureError{err}
				}
				if result.NextCommand != "" {
					_, err = fmt.Fprintln(s.dependencies.Stdout, "Next:", result.NextCommand)
					return wrapFailure(err)
				}
				return nil
			}
			app, err := s.client.Application(cmd.Context(), s.config.AppID)
			if err != nil {
				return failureError{err}
			}
			if app.LastPublishedApplicationSubmission == nil {
				return failureError{errors.New("application has no published submission")}
			}
			// A dry run creates nothing, so a pending submission cannot get in
			// its way — refusing to render a diff because of one leaves you
			// unable to preview a change until you have resolved a draft, which
			// is exactly backwards. It would block the real push, so it is
			// reported as a warning here instead of a failure.
			if pending := app.PendingApplicationSubmission; pending != nil {
				pendingSubmission, pendingErr := s.client.Submission(cmd.Context(), s.config.AppID, pending.ID)
				if pendingErr != nil {
					return failureError{pendingErr}
				}
				switch {
				case !replacePending:
					s.warn(fmt.Sprintf("app already has pending submission %s with status %s; a real push needs the draft resolved or --replace-pending", pending.ID, pendingSubmission.Status))
				case !submissionflow.IsRemovableDraftStatus(pendingSubmission.Status):
					s.warn(fmt.Sprintf("pending submission %s has status %s; only an uncommitted or failed submission can be replaced automatically, so a real push would fail", pending.ID, pendingSubmission.Status))
				}
			}
			source, err := s.client.Submission(cmd.Context(), s.config.AppID, app.LastPublishedApplicationSubmission.ID)
			if err != nil {
				return failureError{err}
			}
			plan, err := metadataflow.BuildPushPlan(dir, directory, source.Raw, allowLocaleRemoval)
			if err != nil {
				return failureError{err}
			}
			hasChanges := plan.HasChanges()
			if releaseNotesPath != "" {
				notesData, readErr := os.ReadFile(releaseNotesPath)
				if readErr != nil {
					return failureError{fmt.Errorf("read release notes: %w", readErr)}
				}
				var notesWarnings []string
				plan.Body, notesWarnings, err = submissionflow.ApplyReleaseNotes(plan.Body, notesData, releaseNotesPath)
				if err != nil {
					return failureError{err}
				}
				// Routed through warn so every warning this command produces
				// ends up in one list, whichever stage raised it.
				for _, warning := range notesWarnings {
					s.warn(warning)
				}
				hasChanges = true
			}
			warnings := s.warnings
			if warnings == nil {
				warnings = []string{}
			}
			result := struct {
				DryRun         bool                         `json:"dryRun"`
				HasChanges     bool                         `json:"hasChanges"`
				ListingChanges []metadataflow.ListingChange `json:"listingChanges"`
				ImageChanges   []metadataflow.ImageChange   `json:"imageChanges"`
				UploadCount    int                          `json:"uploadCount"`
				ReleaseNotes   bool                         `json:"releaseNotes"`
				Body           json.RawMessage              `json:"body"`
				Warnings       []string                     `json:"warnings,omitempty"`
			}{true, hasChanges, plan.ListingChanges, plan.ImageChanges, len(plan.Uploads), releaseNotesPath != "", plan.Body, warnings}
			renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
			if s.format == output.JSON {
				return wrapFailure(renderer.Value(result))
			}
			if err := renderer.Rows(
				[]string{"MODE", "CHANGES", "LISTING CHANGES", "IMAGE CHANGES", "UPLOADS"},
				[][]string{{"dry-run", strconv.FormatBool(result.HasChanges), strconv.Itoa(len(result.ListingChanges)), strconv.Itoa(len(result.ImageChanges)), strconv.Itoa(result.UploadCount)}},
			); err != nil {
				return failureError{err}
			}
			_, err = fmt.Fprintln(s.dependencies.Stdout, "PUT body:", string(result.Body))
			return wrapFailure(err)
		},
	}
	command.Flags().StringVar(&dir, "dir", "", "metadata directory")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "print changes without creating a submission")
	command.Flags().BoolVar(&skipCommit, "skip-commit", false, "create and leave an uncommitted draft")
	command.Flags().BoolVar(&yes, "yes", false, "create and commit the submission")
	command.Flags().StringVar(&releaseNotesPath, "release-notes", "", "release-notes JSON file")
	command.Flags().BoolVar(&replacePending, "replace-pending", false, "delete an existing uncommitted or failed pending submission")
	command.Flags().BoolVar(&allowLocaleRemoval, "allow-locale-removal", false, "allow Store locales missing from the metadata directory to be removed")
	return command
}

func (s *commandState) versionCommand(build BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build information",
		Args:  noArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := s.prepareOutput(); err != nil {
				return err
			}
			renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
			if s.format == output.Table {
				return wrapFailure(renderer.Rows([]string{"VERSION", "COMMIT", "BUILD DATE"}, [][]string{{build.Version, build.Commit, build.Date}}))
			}
			return wrapFailure(renderer.Value(build))
		},
	}
}

func (s *commandState) appCommand() *cobra.Command {
	parent := &cobra.Command{Use: "app", Short: "Inspect the configured app"}
	parent.AddCommand(&cobra.Command{
		Use:   "info",
		Short: "Show application information",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := s.loadApplication(cmd.Context())
			if err != nil {
				return err
			}
			return s.renderApplicationSummary(app)
		},
	})
	return parent
}

func (s *commandState) localesCommand() *cobra.Command {
	parent := &cobra.Command{Use: "locales", Short: "Inspect listing locales"}
	parent.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List locales from the published submission",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := s.loadApplication(cmd.Context())
			if err != nil {
				return err
			}
			ref := app.LastPublishedApplicationSubmission
			if ref == nil {
				ref = app.PendingApplicationSubmission
			}
			if ref == nil {
				return failureError{errors.New("application has no published or pending submission")}
			}
			submission, err := s.client.Submission(cmd.Context(), s.config.AppID, ref.ID)
			if err != nil {
				return failureError{err}
			}
			locales := make([]string, 0, len(submission.Listings))
			for locale := range submission.Listings {
				locales = append(locales, strings.ToLower(locale))
			}
			sort.Strings(locales)
			renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
			if s.format == output.JSON {
				return wrapFailure(renderer.Value(locales))
			}
			rows := make([][]string, len(locales))
			for index, locale := range locales {
				rows[index] = []string{locale}
			}
			return wrapFailure(renderer.Rows([]string{"LOCALE"}, rows))
		},
	})
	return parent
}

func (s *commandState) submissionCommand() *cobra.Command {
	parent := &cobra.Command{Use: "submission", Short: "Inspect and drive submissions"}
	parent.AddCommand(
		s.submissionStatusCommand(), s.submissionGetCommand(), s.submissionDeleteCommand(),
		s.submissionCommitCommand(), s.submissionWatchCommand(),
	)
	return parent
}

func (s *commandState) submissionStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show pending and published submission statuses",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := s.loadApplication(cmd.Context())
			if err != nil {
				return err
			}
			return s.renderSubmissionStatuses(cmd.Context(), app)
		},
	}
}

func (s *commandState) submissionGetCommand() *cobra.Command {
	var id string
	var published, includeUploadURL bool
	command := &cobra.Command{
		Use:   "get",
		Short: "Print raw submission JSON",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id != "" && published {
				return usageError{errors.New("--id and --published are mutually exclusive")}
			}
			app, err := s.loadApplication(cmd.Context())
			if err != nil {
				return err
			}
			submissionID := id
			if submissionID == "" {
				ref := app.PendingApplicationSubmission
				if published {
					ref = app.LastPublishedApplicationSubmission
				}
				if ref == nil {
					return failureError{errors.New("requested submission does not exist")}
				}
				submissionID = ref.ID
			}
			submission, err := s.client.Submission(cmd.Context(), s.config.AppID, submissionID)
			if err != nil {
				return failureError{err}
			}
			data := []byte(submission.Raw)
			if !includeUploadURL {
				data, err = store.RedactUploadURLsJSON(data)
				if err != nil {
					return failureError{fmt.Errorf("redact submission: %w", err)}
				}
			}
			return wrapFailure(output.NewRenderer(s.dependencies.Stdout, s.format).RawJSON(data))
		},
	}
	command.Flags().StringVar(&id, "id", "", "submission id")
	command.Flags().BoolVar(&published, "published", false, "get the last published submission")
	command.Flags().BoolVar(&includeUploadURL, "include-upload-url", false, "include the credential-bearing fileUploadUrl")
	return command
}

func (s *commandState) submissionDeleteCommand() *cobra.Command {
	var yes bool
	command := &cobra.Command{
		Use:   "delete-draft",
		Short: "Delete the pending draft after verifying its state",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return usageError{errors.New("submission delete-draft requires --yes")}
			}
			app, err := s.loadApplication(cmd.Context())
			if err != nil {
				return err
			}
			pending := app.PendingApplicationSubmission
			if pending == nil {
				return failureError{errors.New("application has no pending submission")}
			}
			// The application resource carries no status, so the guard has to
			// read the submission itself before deleting anything.
			submission, getErr := s.client.Submission(cmd.Context(), s.config.AppID, pending.ID)
			if getErr != nil {
				return failureError{getErr}
			}
			status := submission.Status
			// An uncommitted draft, or one whose commit/certification failed:
			// both are finished and both block every new submission until
			// removed. Anything else is genuinely in flight.
			if !submissionflow.IsRemovableDraftStatus(status) {
				return failureError{fmt.Errorf("refusing to delete submission %s in status %s; only an uncommitted or failed submission can be deleted", pending.ID, status)}
			}
			if err := s.prepareManager(); err != nil {
				return err
			}
			if err := s.manager.DeleteDraft(cmd.Context(), s.config.AppID, pending.ID); err != nil {
				return failureError{err}
			}
			renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
			if s.format == output.JSON {
				return wrapFailure(renderer.Value(map[string]string{"deletedSubmissionId": pending.ID}))
			}
			return wrapFailure(renderer.Rows([]string{"DELETED SUBMISSION"}, [][]string{{pending.ID}}))
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, "confirm deletion of the pending draft")
	return command
}

func (s *commandState) submissionCommitCommand() *cobra.Command {
	var id string
	var pollSeconds, pollAttempts int
	command := &cobra.Command{
		Use:   "commit",
		Short: "Commit a prepared draft",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			submissionID, err := s.resolveSubmissionID(cmd.Context(), id, false)
			if err != nil {
				return err
			}
			poll, err := pollOptions(pollSeconds, pollAttempts)
			if err != nil {
				return usageError{err}
			}
			if err := s.prepareManager(); err != nil {
				return err
			}
			result, err := s.manager.Commit(cmd.Context(), s.config.AppID, submissionID, poll)
			if err != nil {
				return failureError{err}
			}
			return s.renderCommitResult(result)
		},
	}
	command.Flags().StringVar(&id, "id", "", "submission id; defaults to pending")
	command.Flags().IntVar(&pollSeconds, "poll-seconds", 30, "seconds between status polls")
	command.Flags().IntVar(&pollAttempts, "poll-attempts", 20, "maximum status polls")
	return command
}

func (s *commandState) submissionWatchCommand() *cobra.Command {
	var id string
	var pollSeconds, pollAttempts int
	command := &cobra.Command{
		Use:   "watch",
		Short: "Watch a submission until it reaches a terminal status",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			submissionID, err := s.resolveSubmissionID(cmd.Context(), id, false)
			if err != nil {
				return err
			}
			poll, err := pollOptions(pollSeconds, pollAttempts)
			if err != nil {
				return usageError{err}
			}
			if err := s.prepareManager(); err != nil {
				return err
			}
			result, err := s.manager.Watch(cmd.Context(), s.config.AppID, submissionID, poll)
			if err != nil {
				return failureError{err}
			}
			renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
			if s.format == output.JSON {
				return wrapFailure(renderer.Value(result))
			}
			if err := renderer.Rows([]string{"STATUS", "CLASSIFICATION", "DETAILS"}, [][]string{{result.Status, string(result.Classification), string(result.StatusDetails)}}); err != nil {
				return failureError{err}
			}
			if result.Warning != "" {
				_, err := fmt.Fprintln(s.dependencies.Stderr, "Warning:", result.Warning)
				return wrapFailure(err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&id, "id", "", "submission id; defaults to pending")
	command.Flags().IntVar(&pollSeconds, "poll-seconds", 30, "seconds between status polls")
	command.Flags().IntVar(&pollAttempts, "poll-attempts", 20, "maximum status polls")
	return command
}

func (s *commandState) rolloutCommand() *cobra.Command {
	parent := &cobra.Command{Use: "rollout", Short: "Inspect and drive package rollouts"}
	parent.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show the last published package rollout",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := s.loadApplication(cmd.Context())
			if err != nil {
				return err
			}
			if app.LastPublishedApplicationSubmission == nil {
				return failureError{errors.New("application has no published submission")}
			}
			rollout, err := s.client.Rollout(cmd.Context(), s.config.AppID, app.LastPublishedApplicationSubmission.ID)
			if err != nil {
				return failureError{err}
			}
			renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
			if s.format == output.JSON {
				return wrapFailure(renderer.Value(rollout))
			}
			return wrapFailure(renderer.Rows(
				[]string{"STATUS", "PERCENTAGE", "FALLBACK SUBMISSION"},
				[][]string{{rollout.PackageRolloutStatus, strconv.FormatFloat(rollout.PackageRolloutPercentage, 'f', -1, 64), rollout.FallbackSubmissionID}},
			))
		},
	}, s.rolloutFinalizeCommand(), s.rolloutSetPercentageCommand(), s.rolloutHaltCommand())
	return parent
}

func (s *commandState) rolloutFinalizeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "finalize",
		Short: "Finalize the last published package rollout",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			submissionID, err := s.resolvePublishedSubmissionID(cmd.Context())
			if err != nil {
				return err
			}
			if err := s.prepareManager(); err != nil {
				return err
			}
			rollout, err := s.manager.FinalizeRollout(cmd.Context(), s.config.AppID, submissionID)
			if err != nil {
				return failureError{err}
			}
			if rollout.PackageRolloutStatus == "" {
				rollout, err = s.client.Rollout(cmd.Context(), s.config.AppID, submissionID)
				if err != nil {
					return failureError{err}
				}
			}
			return s.renderRollout(rollout)
		},
	}
}

func (s *commandState) rolloutSetPercentageCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set-percentage <n>",
		Short: "Set the last published rollout percentage",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError{errors.New("set-percentage requires exactly one percentage")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			percentage, err := strconv.ParseFloat(args[0], 64)
			if err != nil || percentage <= 0 || percentage > 100 {
				return usageError{errors.New("percentage must be a number greater than 0 and at most 100")}
			}
			submissionID, err := s.resolvePublishedSubmissionID(cmd.Context())
			if err != nil {
				return err
			}
			if err := s.prepareManager(); err != nil {
				return err
			}
			rollout, err := s.manager.SetRolloutPercentage(cmd.Context(), s.config.AppID, submissionID, percentage)
			if err != nil {
				return failureError{err}
			}
			return s.renderRollout(rollout)
		},
	}
}

func (s *commandState) rolloutHaltCommand() *cobra.Command {
	var yes bool
	command := &cobra.Command{
		Use:   "halt",
		Short: "Halt the last published package rollout",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return usageError{errors.New("rollout halt requires --yes")}
			}
			submissionID, err := s.resolvePublishedSubmissionID(cmd.Context())
			if err != nil {
				return err
			}
			if err := s.prepareManager(); err != nil {
				return err
			}
			rollout, err := s.manager.HaltRollout(cmd.Context(), s.config.AppID, submissionID)
			if err != nil {
				return failureError{err}
			}
			note := "the next submission will clone the halted submission, not the fallback"
			renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
			if s.format == output.JSON {
				return wrapFailure(renderer.Value(map[string]any{"rollout": rollout, "note": note}))
			}
			if err := renderer.Rows([]string{"STATUS", "PERCENTAGE", "FALLBACK SUBMISSION"}, [][]string{{rollout.PackageRolloutStatus, strconv.FormatFloat(rollout.PackageRolloutPercentage, 'f', -1, 64), rollout.FallbackSubmissionID}}); err != nil {
				return failureError{err}
			}
			_, err = fmt.Fprintln(s.dependencies.Stdout, "Note:", note)
			return wrapFailure(err)
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, "confirm halting the rollout")
	return command
}

func (s *commandState) publishCommand() *cobra.Command {
	parent := &cobra.Command{Use: "publish", Short: "Publish Store packages"}
	var msixPath, releaseNotesPath string
	var rolloutPercentage float64
	var keepExisting, skipCommit, replacePending bool
	var pollSeconds, pollAttempts int
	command := &cobra.Command{
		Use:   "msix",
		Short: "Prepare and optionally commit an MSIX submission",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(msixPath) == "" {
				return usageError{errors.New("publish msix requires --path")}
			}
			if (releaseNotesPath != "") == keepExisting {
				return usageError{errors.New("exactly one of --release-notes or --keep-existing-release-notes is required")}
			}
			if rolloutPercentage <= 0 || rolloutPercentage > 100 {
				return usageError{errors.New("--rollout-percentage must be greater than 0 and at most 100")}
			}
			poll, err := pollOptions(pollSeconds, pollAttempts)
			if err != nil {
				return usageError{err}
			}
			if err := s.preparePublisher(); err != nil {
				return err
			}
			result, err := s.publisher.PublishMSIX(cmd.Context(), submissionflow.PublishMSIXOptions{
				AppID: s.config.AppID, MSIXPath: msixPath, ReleaseNotesPath: releaseNotesPath,
				KeepExistingReleaseNotes: keepExisting, RolloutPercentage: rolloutPercentage,
				SkipCommit: skipCommit, ReplacePending: replacePending, Poll: poll,
			})
			if err != nil {
				return failureError{err}
			}
			renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
			if s.format == output.JSON {
				return wrapFailure(renderer.Value(result))
			}
			status := result.Commit.Status
			if result.Draft {
				status = "PendingCommit draft"
			}
			if err := renderer.Rows(
				[]string{"SUBMISSION", "PACKAGE", "ROLLOUT", "STATUS"},
				[][]string{{result.SubmissionID, result.PackageFileName, strconv.FormatFloat(result.RolloutPercentage, 'f', -1, 64) + "%", status}},
			); err != nil {
				return failureError{err}
			}
			if result.NextCommand != "" {
				_, err = fmt.Fprintln(s.dependencies.Stdout, "Next:", result.NextCommand)
				return wrapFailure(err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&msixPath, "path", "", "path to the MSIX package")
	command.Flags().StringVar(&releaseNotesPath, "release-notes", "", "release-notes JSON file")
	command.Flags().BoolVar(&keepExisting, "keep-existing-release-notes", false, "explicitly retain cloned release notes")
	command.Flags().Float64Var(&rolloutPercentage, "rollout-percentage", 90, "package rollout percentage")
	command.Flags().BoolVar(&skipCommit, "skip-commit", false, "leave an uncommitted draft")
	command.Flags().BoolVar(&replacePending, "replace-pending", false, "delete an existing uncommitted or failed pending submission")
	command.Flags().IntVar(&pollSeconds, "poll-seconds", 30, "seconds between status polls")
	command.Flags().IntVar(&pollAttempts, "poll-attempts", 20, "maximum status polls")
	parent.AddCommand(command)
	return parent
}

func (s *commandState) reviewsCommand() *cobra.Command {
	parent := &cobra.Command{Use: "reviews", Short: "Read Store reviews"}
	var from, to, market, rawFilter, orderBy string
	var top, skip int
	var all bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List application reviews",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if top < 1 || top > 10000 {
				return usageError{errors.New("--top must be between 1 and 10000")}
			}
			if skip < 0 {
				return usageError{errors.New("--skip must be non-negative")}
			}
			startDate, err := reviewDate(from, "1/1/2000")
			if err != nil {
				return usageError{fmt.Errorf("--from: %w", err)}
			}
			endDefault := s.dependencies.Now().Format("1/2/2006")
			endDate, err := reviewDate(to, endDefault)
			if err != nil {
				return usageError{fmt.Errorf("--to: %w", err)}
			}
			if err := s.prepareClient(); err != nil {
				return err
			}
			filter := composeReviewFilter(rawFilter, market)
			page, err := s.client.Reviews(cmd.Context(), store.ReviewQuery{
				ApplicationID: s.config.AppID, StartDate: startDate, EndDate: endDate,
				Top: top, Skip: skip, Filter: filter, OrderBy: orderBy,
			})
			if err != nil {
				return failureError{err}
			}
			reviews := append([]json.RawMessage(nil), page.Value...)
			for all && page.NextLink != "" {
				page, err = s.client.ReviewsNext(cmd.Context(), page.NextLink)
				if err != nil {
					return failureError{err}
				}
				reviews = append(reviews, page.Value...)
			}
			return s.renderReviews(reviews)
		},
	}
	list.Flags().StringVar(&from, "from", "", "start date in YYYY-MM-DD")
	list.Flags().StringVar(&to, "to", "", "end date in YYYY-MM-DD")
	list.Flags().IntVar(&top, "top", 10000, "maximum reviews per page")
	list.Flags().IntVar(&skip, "skip", 0, "review offset")
	list.Flags().BoolVar(&all, "all", false, "follow all continuation pages")
	list.Flags().StringVar(&market, "market", "", "market filter")
	list.Flags().StringVar(&rawFilter, "filter", "", "raw Partner Center filter")
	list.Flags().StringVar(&orderBy, "orderby", "date desc", "review ordering")
	parent.AddCommand(list)
	return parent
}

func (s *commandState) loadApplication(ctx context.Context) (storetypes.Application, error) {
	if err := s.prepareClient(); err != nil {
		return storetypes.Application{}, err
	}
	app, err := s.client.Application(ctx, s.config.AppID)
	if err != nil {
		return storetypes.Application{}, failureError{err}
	}
	return app, nil
}

func (s *commandState) prepareClient() error {
	if s.client != nil {
		return nil
	}
	if err := s.prepareOutput(); err != nil {
		return err
	}
	resolved, err := config.Resolve(config.Overrides{EnvFile: s.envFile, AppID: s.appID}, s.dependencies.Environment)
	if err != nil {
		return usageError{err}
	}
	correlationID, err := newCorrelationID()
	if err != nil {
		return failureError{fmt.Errorf("create correlation id: %w", err)}
	}
	var verboseLog func(string)
	if s.verbose {
		verboseLog = func(message string) { _, _ = fmt.Fprintln(s.dependencies.Stderr, message) }
	}
	client, err := store.NewClient(store.ClientOptions{
		APIBase: resolved.APIBase, LoginBase: resolved.LoginBase, TenantID: resolved.TenantID,
		ClientID: resolved.ClientID, ClientSecret: resolved.ClientSecret,
		HTTPClient: s.dependencies.HTTPClient, Clock: s.dependencies.Clock, Rand: s.dependencies.Rand,
		CorrelationID: correlationID, VerboseLog: verboseLog,
	})
	if err != nil {
		return usageError{err}
	}
	s.config = resolved
	s.client = client
	return nil
}

func (s *commandState) prepareManager() error {
	if s.manager != nil {
		return nil
	}
	if err := s.prepareClient(); err != nil {
		return err
	}
	manager, err := submissionflow.NewManager(submissionflow.ManagerOptions{
		Client: s.client,
		Clock:  s.dependencies.Clock,
		Rand:   s.dependencies.Rand,
	})
	if err != nil {
		return failureError{err}
	}
	s.manager = manager
	return nil
}

func (s *commandState) preparePublisher() error {
	if s.publisher != nil {
		return nil
	}
	if err := s.prepareManager(); err != nil {
		return err
	}
	publisher, err := submissionflow.NewPublisher(submissionflow.PublisherOptions{
		Client: s.client, Manager: s.manager, Uploader: submissionflow.NewAzureBlobUploader(),
		Warn: s.warn,
	})
	if err != nil {
		return failureError{err}
	}
	s.publisher = publisher
	return nil
}

func (s *commandState) prepareListingPublisher() error {
	if s.listingPush != nil {
		return nil
	}
	if err := s.prepareManager(); err != nil {
		return err
	}
	publisher, err := submissionflow.NewListingPublisher(submissionflow.ListingPublisherOptions{
		Client: s.client, Manager: s.manager, Uploader: submissionflow.NewAzureBlobUploader(),
		Warn: s.warn,
	})
	if err != nil {
		return failureError{err}
	}
	s.listingPush = publisher
	return nil
}

func (s *commandState) prepareOutput() error {
	format, err := output.ResolveFormat(s.outputFlag, s.dependencies.IsTTY)
	if err != nil {
		return usageError{err}
	}
	s.format = format
	return nil
}

// renderApplicationSummary shows what the application resource actually
// contains. It deliberately does not show submission statuses: the app resource
// carries only submission ids, so a STATUS column here could only ever be
// blank. `submission status` fetches the real thing.
func (s *commandState) renderApplicationSummary(app storetypes.Application) error {
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	if s.format == output.JSON {
		return wrapFailure(renderer.Value(app))
	}
	rows := [][]string{
		{"id", app.ID},
		{"name", app.PrimaryName},
		{"package family", app.PackageFamilyName},
		{"package identity", app.PackageIdentityName},
		{"publisher", app.PublisherName},
		{"first published", app.FirstPublishedDate},
		{"advanced listing", strconv.FormatBool(app.HasAdvancedListingPermission)},
		{"published submission", submissionRefID(app.LastPublishedApplicationSubmission)},
		{"pending submission", submissionRefID(app.PendingApplicationSubmission)},
	}
	return wrapFailure(renderer.Rows([]string{"FIELD", "VALUE"}, rows))
}

// renderSubmissionStatuses resolves each referenced submission's real status,
// which costs one request per submission but is the only way to get it.
func (s *commandState) renderSubmissionStatuses(ctx context.Context, app storetypes.Application) error {
	type entry struct {
		Type          string          `json:"type"`
		ID            string          `json:"id"`
		Status        string          `json:"status,omitempty"`
		StatusDetails json.RawMessage `json:"statusDetails,omitempty"`
	}
	entries := make([]entry, 0, 2)
	for _, candidate := range []struct {
		label string
		ref   *storetypes.SubmissionReference
	}{
		{"published", app.LastPublishedApplicationSubmission},
		{"pending", app.PendingApplicationSubmission},
	} {
		if candidate.ref == nil {
			continue
		}
		status, err := s.client.SubmissionStatus(ctx, s.config.AppID, candidate.ref.ID)
		if err != nil {
			return failureError{fmt.Errorf("get %s submission status: %w", candidate.label, err)}
		}
		entries = append(entries, entry{
			Type: candidate.label, ID: candidate.ref.ID,
			Status: status.Status, StatusDetails: status.StatusDetails,
		})
	}

	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	if s.format == output.JSON {
		return wrapFailure(renderer.Value(entries))
	}
	rows := make([][]string, 0, len(entries))
	for _, item := range entries {
		rows = append(rows, []string{item.Type, item.ID, item.Status, string(item.StatusDetails)})
	}
	return wrapFailure(renderer.Rows([]string{"TYPE", "ID", "STATUS", "DETAILS"}, rows))
}

func submissionRefID(ref *storetypes.SubmissionReference) string {
	if ref == nil {
		return "-"
	}
	return ref.ID
}

func (s *commandState) renderReviews(rawReviews []json.RawMessage) error {
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	if s.format == output.JSON {
		return wrapFailure(renderer.Value(rawReviews))
	}
	rows := make([][]string, 0, len(rawReviews))
	for _, rawReview := range rawReviews {
		var review storetypes.Review
		if err := json.Unmarshal(rawReview, &review); err != nil {
			return failureError{fmt.Errorf("decode review: %w", err)}
		}
		rows = append(rows, []string{review.Date, review.Market, strconv.Itoa(review.Rating), review.ReviewTitle, truncate(review.ReviewText, 60), review.PackageVersion})
	}
	return wrapFailure(renderer.Rows([]string{"DATE", "MARKET", "RATING", "TITLE", "TEXT", "PACKAGE VERSION"}, rows))
}

func (s *commandState) renderRollout(rollout storetypes.Rollout) error {
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	if s.format == output.JSON {
		return wrapFailure(renderer.Value(rollout))
	}
	return wrapFailure(renderer.Rows(
		[]string{"STATUS", "PERCENTAGE", "FALLBACK SUBMISSION"},
		[][]string{{rollout.PackageRolloutStatus, strconv.FormatFloat(rollout.PackageRolloutPercentage, 'f', -1, 64), rollout.FallbackSubmissionID}},
	))
}

func (s *commandState) renderCommitResult(result submissionflow.CommitResult) error {
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	if s.format == output.JSON {
		return wrapFailure(renderer.Value(result))
	}
	if err := renderer.Rows([]string{"STATUS", "DETAILS"}, [][]string{{result.Status, string(result.StatusDetails)}}); err != nil {
		return failureError{err}
	}
	if result.Warning != "" {
		_, err := fmt.Fprintln(s.dependencies.Stderr, "Warning:", result.Warning)
		return wrapFailure(err)
	}
	return nil
}

func (s *commandState) resolveSubmissionID(ctx context.Context, explicitID string, published bool) (string, error) {
	if explicitID != "" {
		if err := s.prepareClient(); err != nil {
			return "", err
		}
		return explicitID, nil
	}
	app, err := s.loadApplication(ctx)
	if err != nil {
		return "", err
	}
	ref := app.PendingApplicationSubmission
	if published {
		ref = app.LastPublishedApplicationSubmission
	}
	if ref == nil {
		return "", failureError{errors.New("requested submission does not exist")}
	}
	return ref.ID, nil
}

func (s *commandState) resolvePublishedSubmissionID(ctx context.Context) (string, error) {
	return s.resolveSubmissionID(ctx, "", true)
}

func normalizeBuildInfo(info BuildInfo) BuildInfo {
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if info.Date == "" {
		info.Date = "unknown"
	}
	return info
}

func reviewDate(value, fallback string) (string, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return "", errors.New("expected YYYY-MM-DD")
	}
	return parsed.Format("1/2/2006"), nil
}

func pollOptions(seconds, attempts int) (submissionflow.PollOptions, error) {
	if seconds < 0 {
		return submissionflow.PollOptions{}, errors.New("--poll-seconds must be non-negative")
	}
	if attempts < 1 {
		return submissionflow.PollOptions{}, errors.New("--poll-attempts must be positive")
	}
	return submissionflow.PollOptions{Interval: time.Duration(seconds) * time.Second, Attempts: attempts}, nil
}

func composeReviewFilter(rawFilter, market string) string {
	if market == "" {
		return rawFilter
	}
	marketFilter := "market eq '" + strings.ReplaceAll(market, "'", "''") + "'"
	if rawFilter == "" {
		return marketFilter
	}
	return "(" + rawFilter + ") and " + marketFilter
}

func truncate(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum-1]) + "…"
}

func newCorrelationID() (string, error) {
	value := make([]byte, 16)
	if _, err := cryptorand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func noArgs(_ *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageError{fmt.Errorf("unexpected arguments: %s", strings.Join(args, " "))}
	}
	return nil
}

func wrapFailure(err error) error {
	if err == nil {
		return nil
	}
	return failureError{err}
}

func unwrapCommandError(err error) error {
	var usage usageError
	if errors.As(err, &usage) {
		return usage.error
	}
	var failure failureError
	if errors.As(err, &failure) {
		return failure.error
	}
	return err
}

type commandClock struct{}

func (commandClock) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (commandClock) Now() time.Time { return time.Now() }

type commandRand struct{}

func (commandRand) Float64() float64 { return mathrand.Float64() }
