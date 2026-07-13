// Package cli defines the pcenter Cobra command tree.
package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/prof18/pcenter-cli/internal/config"
	"github.com/prof18/pcenter-cli/internal/output"
	"github.com/prof18/pcenter-cli/internal/store"
	storetypes "github.com/prof18/pcenter-cli/internal/store/types"
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
}

type commandState struct {
	dependencies Dependencies
	outputFlag   string
	envFile      string
	appID        string
	verbose      bool
	format       output.Format
	client       *store.Client
	config       config.Config
}

type usageError struct{ error }
type failureError struct{ error }

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
		output.WriteError(dependencies.Stderr, format, unwrapCommandError(err))
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
	)
	return root
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

func (s *commandState) authCommand() *cobra.Command {
	parent := &cobra.Command{Use: "auth", Short: "Check authentication"}
	parent.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Acquire a token and fetch the configured app",
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
	parent := &cobra.Command{Use: "submission", Short: "Inspect submissions"}
	parent.AddCommand(s.submissionStatusCommand(), s.submissionGetCommand())
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
			return s.renderApplicationSummary(app)
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

func (s *commandState) rolloutCommand() *cobra.Command {
	parent := &cobra.Command{Use: "rollout", Short: "Inspect package rollouts"}
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
	})
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
		HTTPClient: s.dependencies.HTTPClient, CorrelationID: correlationID, VerboseLog: verboseLog,
	})
	if err != nil {
		return usageError{err}
	}
	s.config = resolved
	s.client = client
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

func (s *commandState) renderApplicationSummary(app storetypes.Application) error {
	type summary struct {
		ID        string                          `json:"id"`
		Name      string                          `json:"name"`
		Published *storetypes.SubmissionReference `json:"published,omitempty"`
		Pending   *storetypes.SubmissionReference `json:"pending,omitempty"`
	}
	value := summary{ID: app.ID, Name: app.Name, Published: app.LastPublishedApplicationSubmission, Pending: app.PendingApplicationSubmission}
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	if s.format == output.JSON {
		return wrapFailure(renderer.Value(value))
	}
	rows := make([][]string, 0, 2)
	if value.Published != nil {
		rows = append(rows, []string{"published", value.Published.ID, value.Published.Status, string(value.Published.StatusDetails)})
	}
	if value.Pending != nil {
		rows = append(rows, []string{"pending", value.Pending.ID, value.Pending.Status, string(value.Pending.StatusDetails)})
	}
	return wrapFailure(renderer.Rows([]string{"TYPE", "ID", "STATUS", "DETAILS"}, rows))
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
	if _, err := rand.Read(value); err != nil {
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
