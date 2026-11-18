package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	Storage   string   `json:"storage"`
	Stored    []string `json:"stored"`
	Kept      []string `json:"kept"`
	Validated bool     `json:"validated"`
	Warnings  []string `json:"warnings,omitempty"`
}

const (
	storageKeychain = "keychain"
	storageFile     = "file"
)

const keychainLabel = "system keychain (service " + config.KeychainService + ", account " + config.KeychainAccount + ")"

const noKeychainRemedy = "rerun with --storage file, and set " + config.BypassKeychainEnv + "=1 if this machine has no keychain"

func (s *commandState) authLoginCommand() *cobra.Command {
	var overrides config.Overrides
	var skipValidation bool
	var storage string

	command := &cobra.Command{
		Use:   "login",
		Short: "Store Partner Center credentials in the keychain or the credentials file",
		Long: strings.TrimSpace(`
Store Partner Center credentials so other commands can find them.

Values are taken from flags when given, and prompted for otherwise — but only
when stdin is a terminal, so scripts and CI never hang. Passing every value as
a flag stops pcenter prompting, but reading the system keychain can still make
the operating system show an unlock prompt: unattended logins set
PCENTER_BYPASS_KEYCHAIN=1 and write the credentials file.

In GitHub Actions prefer the MS_STORE_* environment variables straight from
your secrets: they need no login on the runner and take precedence over
stored credentials.

Credentials go to the system keychain (service pcenter, account default). They
go to ~/.config/pcenter/credentials.env instead, written readable only by you,
with --storage file, when --env-file or PCENTER_ENV_FILE names a file, or when
PCENTER_BYPASS_KEYCHAIN=1 is set. Login never falls back from the keychain to
the file: if the keychain cannot be used, it fails and says so. Stored values
are kept unless a flag replaces them.

The app id is optional here: one account can publish several apps, so it is
usually better passed per command with --app-id.`),
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := s.prepareOutput(); err != nil {
				return err
			}
			// The store is the thing being written, so flag credentials must
			// not be read back out of the environment here.
			report, err := config.Inspect(config.Overrides{EnvFile: s.envFile}, s.dependencies.Environment, s.dependencies.Keychain)
			if err != nil {
				return usageError{err}
			}
			target, explicitKeychain, err := s.chooseLoginStorage(cmd, storage, report)
			if err != nil {
				return err
			}
			s.refreshKeychainForLogin(&report)
			if target == storageKeychain && report.KeychainState == config.KeychainUnavailable {
				return failureError{fmt.Errorf("could not read the system keychain (%s); %s", report.KeychainError, noKeychainRemedy)}
			}

			flagValues := map[string]string{
				config.TenantIDName:     overrides.TenantID,
				config.ClientIDName:     overrides.ClientID,
				config.ClientSecretName: overrides.ClientSecret,
				config.AppIDName:        firstNonEmptyString(overrides.AppID, s.appID),
			}

			values := map[string]string{}
			result := authLoginResult{EnvFile: report.EnvFile, Storage: target}
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

			payload, err := loginPayload(report, values, target)
			if err != nil {
				return usageError{err}
			}
			if target == storageKeychain && report.KeychainState == config.KeychainInvalid && !holdsAccountValue(payload) {
				// Nothing would be written, so the broken item would survive a
				// login that reports success.
				return usageError{errors.New("the keychain item is not valid and nothing here would replace it; pass --tenant-id, --client-id and --client-secret to store a new one")}
			}

			if !skipValidation {
				if err := s.verifyCredentials(cmd.Context(), merged, report.Config); err != nil {
					return failureError{fmt.Errorf("credentials were not saved: %w", err)}
				}
				result.Validated = true
			}

			if err := s.writeLoginPayload(target, report, payload); err != nil {
				return err
			}
			s.warnAboutLoginPrecedence(target, explicitKeychain, report)
			result.Warnings = s.warnings
			return s.renderLoginResult(result)
		},
	}

	command.Flags().StringVar(&overrides.TenantID, "tenant-id", "", "Azure AD tenant id")
	command.Flags().StringVar(&overrides.ClientID, "client-id", "", "Azure AD application (client) id")
	command.Flags().StringVar(&overrides.ClientSecret, "client-secret", "", "Azure AD client secret")
	command.Flags().StringVar(&overrides.AppID, "app-id", "", "Microsoft Store product id (optional)")
	command.Flags().BoolVar(&skipValidation, "skip-validation", false, "save without checking the credentials against the Store")
	command.Flags().StringVar(&storage, "storage", "", "where to store credentials: keychain or file (default keychain, or file when --env-file or PCENTER_ENV_FILE is set)")
	return command
}

// chooseLoginStorage applies the storage rule: the bypass means file, then an
// explicit --storage, then file when the env file was named, then keychain.
func (s *commandState) chooseLoginStorage(cmd *cobra.Command, storage string, report config.Report) (target string, explicitKeychain bool, err error) {
	explicit := cmd.Flags().Changed("storage")
	if explicit && storage != storageKeychain && storage != storageFile {
		return "", false, usageError{fmt.Errorf("--storage must be keychain or file, not %q", storage)}
	}
	bypass := config.BypassKeychain(s.dependencies.Environment)
	switch {
	case bypass && explicit && storage == storageKeychain:
		return "", false, usageError{fmt.Errorf("--storage keychain conflicts with %s; unset it or use --storage file", config.BypassKeychainEnv)}
	case bypass:
		target = storageFile
	case explicit:
		target = storage
	case report.EnvFileExplicit:
		target = storageFile
	default:
		target = storageKeychain
	}
	explicitKeychain = explicit && target == storageKeychain
	if explicitKeychain && report.EnvFileExplicit && !report.EnvFileExists {
		return "", false, usageError{fmt.Errorf(
			"%s is named by --env-file or PCENTER_ENV_FILE but does not exist, so other commands would fail; unset it or use --storage file",
			report.EnvFile)}
	}
	return target, explicitKeychain, nil
}

// refreshKeychainForLogin makes sure login knows what the keychain holds, even
// when the environment set everything and Inspect skipped the lookup.
func (s *commandState) refreshKeychainForLogin(report *config.Report) {
	if config.BypassKeychain(s.dependencies.Environment) {
		return
	}
	if s.dependencies.Keychain == nil {
		report.KeychainState = config.KeychainUnavailable
		report.KeychainError = "no system keychain"
		return
	}
	if report.KeychainState != config.KeychainNotRead {
		return
	}
	values, err := config.LoadKeychain(s.dependencies.Keychain)
	var formatErr *config.KeychainFormatError
	switch {
	case errors.As(err, &formatErr):
		report.KeychainState = config.KeychainInvalid
		report.KeychainError = err.Error()
	case err != nil:
		report.KeychainState = config.KeychainUnavailable
		report.KeychainError = err.Error()
	case len(values) > 0:
		report.KeychainHasAny = true
	}
}

// loginPayload is what login writes: the new values plus every value kept from
// a store, so the target store alone holds the account login validates.
func loginPayload(report config.Report, values map[string]string, target string) (map[string]string, error) {
	payload := map[string]string{}
	for _, name := range config.AllNames {
		switch {
		case values[name] != "":
			payload[name] = values[name]
		case report.Sources[name] == config.SourceFile || report.Sources[name] == config.SourceKeychain:
			payload[name] = report.Value(name)
		}
	}

	if holdsAccountValue(payload) {
		for _, name := range config.AccountNames {
			if report.Sources[name] == config.SourceEnv && values[name] == "" {
				return nil, fmt.Errorf("%s comes from the environment and would not be stored; pass it as a flag or unset it", name)
			}
		}
	}

	for _, name := range config.AllNames {
		value := payload[name]
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("%s contains a line break, which cannot be stored", name)
		}
		if target == storageFile && value != "" &&
			(value != strings.TrimSpace(value) || strings.HasPrefix(value, `"`) || strings.HasPrefix(value, "'")) {
			return nil, fmt.Errorf("%s starts with a quote or has surrounding spaces, which the credentials file cannot store unchanged; use --storage keychain", name)
		}
	}
	return payload, nil
}

// holdsAccountValue reports whether the payload sets any account credential.
func holdsAccountValue(payload map[string]string) bool {
	for _, name := range config.AccountNames {
		if payload[name] != "" {
			return true
		}
	}
	return false
}

func (s *commandState) writeLoginPayload(target string, report config.Report, payload map[string]string) error {
	if len(payload) == 0 {
		return nil
	}
	if target == storageKeychain {
		if err := config.SaveKeychain(s.dependencies.Keychain, payload); err != nil {
			return failureError{fmt.Errorf("store credentials in the keychain: %w; %s", err, noKeychainRemedy)}
		}
		return nil
	}
	if err := config.Save(report.EnvFile, payload); err != nil {
		return failureError{err}
	}
	return nil
}

func (s *commandState) warnAboutLoginPrecedence(target string, explicitKeychain bool, report config.Report) {
	if explicitKeychain && report.EnvFileExplicit && report.EnvFileExists {
		s.warn(fmt.Sprintf("%s is named by --env-file or PCENTER_ENV_FILE, so other commands read it before the keychain", report.EnvFile))
	}
	if target != storageFile || report.EnvFileExplicit {
		return
	}
	switch {
	case report.KeychainHasAny:
		s.warn(fmt.Sprintf("the system keychain also holds credentials and takes precedence over %s; run \"pcenter auth login --storage keychain\" to update it instead", report.EnvFile))
	case report.KeychainState == config.KeychainUnavailable && !config.BypassKeychain(s.dependencies.Environment):
		s.warn(fmt.Sprintf("could not read the system keychain; if it holds credentials, they take precedence over %s", report.EnvFile))
	}
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
			report, err := config.Inspect(config.Overrides{EnvFile: s.envFile, AppID: s.appID}, s.dependencies.Environment, s.dependencies.Keychain)
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
			s.inspected = &report
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
			report, inspectErr := config.Inspect(config.Overrides{EnvFile: s.envFile, AppID: s.appID}, s.dependencies.Environment, s.dependencies.Keychain)
			if inspectErr != nil {
				// A malformed file is itself the diagnosis, so report it as a
				// finding rather than failing before anything is printed.
				return s.renderChecks([]authCheck{{
					Check: "env file", Status: "fail", Detail: inspectErr.Error(),
					Path: s.envFile, Remedy: "pcenter auth login",
				}}, true)
			}

			s.inspected = &report
			keychain, keychainFailed := keychainCheck(report)
			checks := []authCheck{s.envFileCheck(report), keychain}
			if shadow, ok := shadowedFileCheck(report); ok {
				checks = append(checks, shadow)
			}
			for _, name := range config.AllNames {
				checks = append(checks, credentialCheck(report, name))
			}
			setupFailed := keychainFailed || report.EnvFileError != ""

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
			degraded := setupFailed
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
		Short: "Delete stored credentials from the keychain and the credentials file",
		Args:  noArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := s.prepareOutput(); err != nil {
				return err
			}
			if !confirmed {
				return usageError{errors.New("refusing to delete credentials without --yes")}
			}
			// Only the path is needed, so a malformed or unreadable file cannot
			// stop logout from removing it and the keychain item.
			envFile, fileErr := config.EnvFilePath(config.Overrides{EnvFile: s.envFile}, s.dependencies.Environment)
			removed := false
			if fileErr == nil {
				removed, fileErr = removeCredentialsFile(envFile)
			}
			keychainState, keychainErr := s.removeKeychainItem()

			var failures []error
			if fileErr != nil {
				failures = append(failures, fmt.Errorf("remove %s: %w", envFile, fileErr))
			}
			if keychainErr != nil {
				failures = append(failures, fmt.Errorf("remove keychain item: %w", keychainErr))
			}
			if len(failures) > 0 {
				return failureError{errors.Join(failures...)}
			}

			renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
			fileState := "removed"
			if !removed {
				fileState = "nothing to remove"
			}
			if s.format == output.Table {
				return wrapFailure(renderer.Rows([]string{"LOCATION", "RESULT"}, [][]string{
					{envFile, fileState},
					{keychainLabel, keychainState},
				}))
			}
			return wrapFailure(renderer.Value(map[string]any{"envFile": envFile, "removed": removed, "keychain": keychainState}))
		},
	}
	command.Flags().BoolVar(&confirmed, "yes", false, "confirm deleting stored credentials")
	return command
}

// removeCredentialsFile deletes the credentials file but refuses a directory,
// because os.Remove would delete an empty one.
func removeCredentialsFile(path string) (bool, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return false, fmt.Errorf("%s is a directory, not a credentials file", path)
	}
	return config.Remove(path)
}

// removeKeychainItem deletes pcenter's keychain item. Any failure is an error,
// whatever the keychain state, because a locked keychain can still hold
// credentials that work again once it is unlocked.
func (s *commandState) removeKeychainItem() (string, error) {
	if config.BypassKeychain(s.dependencies.Environment) {
		return "bypassed", nil
	}
	if s.dependencies.Keychain == nil {
		return "unavailable", nil
	}
	deleted, err := config.DeleteKeychain(s.dependencies.Keychain)
	if err != nil {
		return "", fmt.Errorf("%w; unlock the keychain and run logout again, or set %s=1 where there is no keychain", err, config.BypassKeychainEnv)
	}
	if deleted {
		return "removed", nil
	}
	return "nothing to remove", nil
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
	case report.EnvFileError != "":
		check.Status = "fail"
		check.Detail = report.EnvFileError
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

// keychainCheck reports the keychain lookup. Only an invalid item fails the
// setup: an unreadable keychain is fine when other sources supply everything.
func keychainCheck(report config.Report) (authCheck, bool) {
	check := authCheck{Check: "keychain", Detail: string(report.KeychainState)}
	switch report.KeychainState {
	case config.KeychainFound:
		check.Status, check.Detail = "ok", keychainLabel
	case config.KeychainEmpty:
		check.Status, check.Detail, check.Remedy = "none", "no pcenter item in the "+keychainLabel, "pcenter auth login"
	case config.KeychainNotRead:
		check.Status, check.Detail = "skip", "not read: flags, environment and env file set everything"
	case config.KeychainBypassed:
		check.Status, check.Detail = "skip", config.BypassKeychainEnv+" is set"
	case config.KeychainUnavailable:
		check.Status, check.Detail = "warn", report.KeychainError
	case config.KeychainInvalid:
		check.Status, check.Detail, check.Remedy = "fail", report.KeychainError, "pcenter auth login --storage keychain"
		return check, true
	}
	return check, false
}

// shadowedFileCheck warns when the keychain hides a default credentials file
// that still holds an account, which the keychain silently overrides.
func shadowedFileCheck(report config.Report) (authCheck, bool) {
	if report.EnvFileExplicit || !report.KeychainHasAccount || !report.EnvFileHasAccount {
		return authCheck{}, false
	}
	inKeychain := map[string]bool{}
	for _, name := range report.KeychainNames {
		inKeychain[name] = true
	}
	var onlyInFile []string
	for _, name := range report.EnvFileNames {
		if !inKeychain[name] {
			onlyInFile = append(onlyInFile, name)
		}
	}
	detail := fmt.Sprintf("the keychain holds credentials, so this run ignores %s; delete it only if no script names it with --env-file or PCENTER_ENV_FILE", report.EnvFile)
	if len(onlyInFile) > 0 {
		detail = fmt.Sprintf("the keychain overrides the account credentials in %s; keep the file, because only it sets %s", report.EnvFile, strings.Join(onlyInFile, ", "))
	}
	return authCheck{Check: "shadowed file", Status: "warn", Path: report.EnvFile, Detail: detail}, true
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
	rows := [][]string{{"storage", result.Storage}}
	if result.Storage == storageFile {
		rows = append(rows, []string{"file", result.EnvFile})
	}
	rows = append(rows, [][]string{
		{"stored", joinOrDash(result.Stored)},
		{"kept", joinOrDash(result.Kept)},
		{"validated", validated},
	}...)
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
		"keychain":      string(report.KeychainState),
	}
	if report.KeychainError != "" {
		payload["keychainError"] = report.KeychainError
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
	rows = append(rows, []string{"keychain", string(report.KeychainState)})
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
