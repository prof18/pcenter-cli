package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/prof18/pcenter-cli/internal/config"
	"github.com/prof18/pcenter-cli/internal/output"
	"github.com/prof18/pcenter-cli/internal/store"
	storetypes "github.com/prof18/pcenter-cli/internal/store/types"
)

// authCommand groups everything about getting credentials in place and proving
// they work. Operational commands stay non-interactive; login is the one place
// pcenter may prompt, and only when a terminal is attached.
func (s *commandState) authCommand() *cobra.Command {
	parent := &cobra.Command{Use: "auth", Short: "Set up and check authentication"}
	parent.AddCommand(
		s.authLoginCommand(),
		s.authStatusCommand(),
		s.authDoctorCommand(),
		s.authLogoutCommand(),
	)
	return parent
}

type authLoginResult struct {
	EnvFile   string   `json:"envFile"`
	Stored    []string `json:"stored"`
	Kept      []string `json:"kept"`
	Validated bool     `json:"validated"`
}

func (s *commandState) authLoginCommand() *cobra.Command {
	var overrides config.Overrides
	var skipValidation bool

	command := &cobra.Command{
		Use:   "login",
		Short: "Store Partner Center credentials in the credentials file",
		Long: strings.TrimSpace(`
Store Partner Center credentials so other commands can find them.

Values are taken from flags when given, and prompted for otherwise — but only
when stdin is a terminal, so scripts and CI never hang. Pass every value as a
flag to run this unattended.

In GitHub Actions prefer the MS_STORE_* environment variables straight from
your secrets: they need no file on the runner and take precedence over one.

Credentials go to ~/.config/pcenter/credentials.env unless --env-file or
PCENTER_ENV_FILE says otherwise, written readable only by you. Values already
in the file are kept unless a flag replaces them.

The app id is optional here: one account can publish several apps, so it is
usually better passed per command with --app-id.`),
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := s.prepareOutput(); err != nil {
				return err
			}
			// The env file is the thing being written, so flag credentials must
			// not be read back out of the environment here.
			report, err := config.Inspect(config.Overrides{EnvFile: s.envFile}, s.dependencies.Environment)
			if err != nil {
				return usageError{err}
			}

			flagValues := map[string]string{
				config.TenantIDName:     overrides.TenantID,
				config.ClientIDName:     overrides.ClientID,
				config.ClientSecretName: overrides.ClientSecret,
				config.AppIDName:        firstNonEmptyString(overrides.AppID, s.appID),
			}

			values := map[string]string{}
			result := authLoginResult{EnvFile: report.EnvFile}
			for _, name := range config.AllNames {
				existing := report.Value(name)
				switch {
				case flagValues[name] != "":
					values[name] = flagValues[name]
					result.Stored = append(result.Stored, name)
				case existing != "":
					result.Kept = append(result.Kept, name)
				case name == config.AppIDName:
					// Optional: an account can publish several apps.
				default:
					entered, promptErr := s.promptFor(name)
					if promptErr != nil {
						return usageError{promptErr}
					}
					values[name] = entered
					result.Stored = append(result.Stored, name)
				}
			}

			merged := mergeCredentials(report, values)
			if missing := missingFrom(merged, config.AccountNames); len(missing) > 0 {
				return usageError{fmt.Errorf("cannot save incomplete credentials: %s still unset", strings.Join(missing, ", "))}
			}

			if !skipValidation {
				if err := s.verifyCredentials(cmd.Context(), merged, report.Config); err != nil {
					return failureError{fmt.Errorf("credentials were not saved: %w", err)}
				}
				result.Validated = true
			}

			if err := config.Save(report.EnvFile, values); err != nil {
				return failureError{err}
			}
			return s.renderLoginResult(result)
		},
	}

	command.Flags().StringVar(&overrides.TenantID, "tenant-id", "", "Azure AD tenant id")
	command.Flags().StringVar(&overrides.ClientID, "client-id", "", "Azure AD application (client) id")
	command.Flags().StringVar(&overrides.ClientSecret, "client-secret", "", "Azure AD client secret")
	command.Flags().StringVar(&overrides.AppID, "app-id", "", "Microsoft Store product id (optional)")
	command.Flags().BoolVar(&skipValidation, "skip-validation", false, "save without checking the credentials against the Store")
	return command
}

func (s *commandState) authStatusCommand() *cobra.Command {
	var offline bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Show where credentials resolve from, then acquire a token",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := s.prepareOutput(); err != nil {
				return err
			}
			report, err := config.Inspect(config.Overrides{EnvFile: s.envFile, AppID: s.appID}, s.dependencies.Environment)
			if err != nil {
				return usageError{err}
			}
			// Table output can print resolution first and then the app. JSON
			// output must stay a single document, so the app is folded in
			// below rather than rendered separately.
			if s.format == output.Table {
				if err := s.renderResolution(report); err != nil {
					return err
				}
			}
			if offline {
				if s.format != output.Table {
					return s.renderResolutionJSON(report, nil)
				}
				return nil
			}
			if missing := report.Missing(config.AllNames); len(missing) > 0 {
				return usageError{&config.MissingConfigError{
					Missing:       missing,
					EnvFile:       report.EnvFile,
					EnvFileExists: report.EnvFileExists,
				}}
			}
			app, err := s.loadApplication(cmd.Context())
			if err != nil {
				return err
			}
			if s.format != output.Table {
				return s.renderResolutionJSON(report, &app)
			}
			return s.renderApplicationSummary(app)
		},
	}
	command.Flags().BoolVar(&offline, "offline", false, "report resolution only, without contacting the Store")
	return command
}

// authCheck keeps the human sentence and the machine fields side by side, the
// same split the error payload uses: `detail` is what a person reads in the
// table, everything else is what automation acts on without parsing it.
type authCheck struct {
	Check  string `json:"check"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	// Source is where a setting resolved from: flag, environment, env-file.
	Source string `json:"source,omitempty"`
	// Value is the resolved value, omitted for secrets.
	Value string `json:"value,omitempty"`
	// Path and Mode describe the credentials file when the check concerns it.
	Path string `json:"path,omitempty"`
	Mode string `json:"mode,omitempty"`
	// Remedy is a command that fixes this check.
	Remedy string `json:"remedy,omitempty"`
}

// authReport is an object rather than a bare array so a caller reads `.ok`
// instead of scanning every status to decide whether the setup is usable.
type authReport struct {
	OK     bool        `json:"ok"`
	Checks []authCheck `json:"checks"`
}

func (s *commandState) authDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose credential setup and report what to fix",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := s.prepareOutput(); err != nil {
				return err
			}
			report, inspectErr := config.Inspect(config.Overrides{EnvFile: s.envFile, AppID: s.appID}, s.dependencies.Environment)
			if inspectErr != nil {
				// A malformed file is itself the diagnosis, so report it as a
				// finding rather than failing before anything is printed.
				return s.renderChecks([]authCheck{{
					Check: "env file", Status: "fail", Detail: inspectErr.Error(),
					Path: s.envFile, Remedy: "pcenter auth login",
				}}, true)
			}

			checks := []authCheck{s.envFileCheck(report)}
			for _, name := range config.AllNames {
				checks = append(checks, credentialCheck(report, name))
			}

			failed := report.Missing(config.AccountNames)
			if len(failed) > 0 {
				checks = append(checks, authCheck{
					Check:  "token",
					Status: "skip",
					Detail: "not attempted: account credentials incomplete",
					Remedy: "pcenter auth login",
				})
				return s.renderChecks(checks, true)
			}

			tokenCheck := authCheck{Check: "token", Status: "ok", Detail: "acquired from " + report.Config.LoginBase, Source: report.Config.LoginBase}
			degraded := false
			if err := s.verifyCredentials(cmd.Context(), credentialMap(report), report.Config); err != nil {
				tokenCheck = authCheck{Check: "token", Status: "fail", Detail: err.Error(), Remedy: "pcenter auth login"}
				degraded = true
			}
			checks = append(checks, tokenCheck)

			switch {
			case degraded:
			case report.Config.AppID == "":
				checks = append(checks, authCheck{
					Check: "app", Status: "skip",
					Detail: "no app id configured; pass --app-id or set " + config.AppIDName,
					Remedy: "pcenter auth login --app-id <product-id>",
				})
			default:
				appCheck := authCheck{Check: "app", Status: "ok", Value: report.Config.AppID, Detail: "reachable: " + report.Config.AppID}
				if _, err := s.loadApplication(cmd.Context()); err != nil {
					appCheck = authCheck{Check: "app", Status: "fail", Value: report.Config.AppID, Detail: err.Error()}
					degraded = true
				}
				checks = append(checks, appCheck)
			}
			return s.renderChecks(checks, degraded)
		},
	}
}

func (s *commandState) authLogoutCommand() *cobra.Command {
	var confirmed bool
	command := &cobra.Command{
		Use:   "logout",
		Short: "Delete the credentials file",
		Args:  noArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := s.prepareOutput(); err != nil {
				return err
			}
			if !confirmed {
				return usageError{errors.New("refusing to delete credentials without --yes")}
			}
			report, err := config.Inspect(config.Overrides{EnvFile: s.envFile}, s.dependencies.Environment)
			if err != nil {
				return usageError{err}
			}
			removed, err := config.Remove(report.EnvFile)
			if err != nil {
				return failureError{err}
			}
			renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
			state := "removed"
			if !removed {
				state = "nothing to remove"
			}
			if s.format == output.Table {
				return wrapFailure(renderer.Rows([]string{"FILE", "RESULT"}, [][]string{{report.EnvFile, state}}))
			}
			return wrapFailure(renderer.Value(map[string]any{"envFile": report.EnvFile, "removed": removed}))
		},
	}
	command.Flags().BoolVar(&confirmed, "yes", false, "confirm deleting the credentials file")
	return command
}

// verifyCredentials proves credentials before they are written, so a typo is
// caught here rather than by the next command to run.
func (s *commandState) verifyCredentials(ctx context.Context, values map[string]string, base config.Config) error {
	correlationID, err := newCorrelationID()
	if err != nil {
		return fmt.Errorf("create correlation id: %w", err)
	}
	client, err := store.NewClient(store.ClientOptions{
		APIBase: base.APIBase, LoginBase: base.LoginBase,
		TenantID: values[config.TenantIDName], ClientID: values[config.ClientIDName],
		ClientSecret: values[config.ClientSecretName],
		HTTPClient:   s.dependencies.HTTPClient, Clock: s.dependencies.Clock, Rand: s.dependencies.Rand,
		CorrelationID: correlationID,
	})
	if err != nil {
		return err
	}
	return client.VerifyCredentials(ctx)
}

func (s *commandState) envFileCheck(report config.Report) authCheck {
	check := authCheck{Check: "env file", Path: report.EnvFile}
	switch {
	case !report.EnvFileExists:
		check.Status = "none"
		check.Detail = report.EnvFile + " does not exist (fine if MS_STORE_* are set in the environment)"
		check.Remedy = "pcenter auth login"
	case report.WorldReadable():
		check.Status = "warn"
		check.Mode = fmt.Sprintf("%04o", report.EnvFileMode.Perm())
		check.Detail = fmt.Sprintf("%s is mode %s; it holds a client secret, so chmod 600 it", report.EnvFile, check.Mode)
		check.Remedy = "chmod 600 " + report.EnvFile
	default:
		check.Status = "ok"
		check.Mode = fmt.Sprintf("%04o", report.EnvFileMode.Perm())
		check.Detail = report.EnvFile
	}
	return check
}

func credentialCheck(report config.Report, name string) authCheck {
	source := report.Sources[name]
	if source == config.SourceUnset {
		if name == config.AppIDName {
			return authCheck{
				Check: name, Status: "none",
				Detail: "not set; pass --app-id per command, or store one with auth login --app-id",
				Remedy: "pcenter auth login --app-id <product-id>",
			}
		}
		return authCheck{
			Check: name, Status: "fail", Detail: "not set",
			Remedy: "pcenter auth login",
		}
	}
	check := authCheck{Check: name, Status: "ok", Source: string(source), Detail: "from " + string(source)}
	if !config.SecretNames[name] {
		check.Value = report.Value(name)
		check.Detail += ": " + check.Value
	}
	return check
}

func credentialMap(report config.Report) map[string]string {
	values := make(map[string]string, len(config.AllNames))
	for _, name := range config.AllNames {
		values[name] = report.Value(name)
	}
	return values
}

func mergeCredentials(report config.Report, values map[string]string) map[string]string {
	merged := credentialMap(report)
	for key, value := range values {
		merged[key] = value
	}
	return merged
}

func missingFrom(values map[string]string, names []string) []string {
	missing := make([]string, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(values[name]) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

func (s *commandState) promptFor(name string) (string, error) {
	if !s.dependencies.IsTTY {
		return "", fmt.Errorf("%s is not set and stdin is not a terminal; pass --%s", name, flagNameFor(name))
	}
	prompt := fmt.Sprintf("%s: ", name)
	if config.SecretNames[name] {
		value, err := s.dependencies.PromptSecret(prompt)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s cannot be empty", name)
		}
		return value, nil
	}
	value, err := s.dependencies.PromptLine(prompt)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s cannot be empty", name)
	}
	return strings.TrimSpace(value), nil
}

func flagNameFor(name string) string {
	switch name {
	case config.TenantIDName:
		return "tenant-id"
	case config.ClientIDName:
		return "client-id"
	case config.ClientSecretName:
		return "client-secret"
	case config.AppIDName:
		return "app-id"
	}
	return strings.ToLower(name)
}

func (s *commandState) renderLoginResult(result authLoginResult) error {
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	if s.format != output.Table {
		return wrapFailure(renderer.Value(result))
	}
	validated := "skipped"
	if result.Validated {
		validated = "yes"
	}
	rows := [][]string{
		{"file", result.EnvFile},
		{"stored", joinOrDash(result.Stored)},
		{"kept", joinOrDash(result.Kept)},
		{"validated", validated},
	}
	return wrapFailure(renderer.Rows([]string{"FIELD", "VALUE"}, rows))
}

// renderResolutionJSON emits resolution and the application as one document,
// because a JSON consumer parses a single value from stdout.
func (s *commandState) renderResolutionJSON(report config.Report, app *storetypes.Application) error {
	sources := make(map[string]string, len(config.AllNames))
	for _, name := range config.AllNames {
		sources[name] = string(report.Sources[name])
	}
	payload := map[string]any{
		"envFile":       report.EnvFile,
		"envFileExists": report.EnvFileExists,
		"sources":       sources,
	}
	if app != nil {
		payload["application"] = map[string]any{
			"id":        app.ID,
			"name":      app.PrimaryName,
			"published": app.LastPublishedApplicationSubmission,
			"pending":   app.PendingApplicationSubmission,
		}
	}
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	return wrapFailure(renderer.Value(payload))
}

func (s *commandState) renderResolution(report config.Report) error {
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	rows := make([][]string, 0, len(config.AllNames))
	for _, name := range config.AllNames {
		source := string(report.Sources[name])
		if source == "" {
			source = "not set"
		}
		rows = append(rows, []string{name, source})
	}
	return wrapFailure(renderer.Rows([]string{"SETTING", "SOURCE"}, rows))
}

func (s *commandState) renderChecks(checks []authCheck, degraded bool) error {
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	if s.format == output.Table {
		rows := make([][]string, 0, len(checks))
		for _, check := range checks {
			rows = append(rows, []string{check.Check, check.Status, check.Detail})
		}
		if err := renderer.Rows([]string{"CHECK", "STATUS", "DETAIL"}, rows); err != nil {
			return wrapFailure(err)
		}
	} else if err := renderer.Value(authReport{OK: !degraded, Checks: checks}); err != nil {
		return wrapFailure(err)
	}
	if degraded {
		return failureError{errors.New("credential setup is not usable; see the checks above")}
	}
	return nil
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
