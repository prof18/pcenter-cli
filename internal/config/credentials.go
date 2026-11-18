package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/prof18/pcenter-cli/internal/output"
)

// Source names where a resolved value came from. Commands report it so a
// surprising credential can be traced without printing the value itself.
type Source string

// Credential sources, in the order they take precedence.
const (
	SourceUnset Source = ""
	SourceFlag  Source = "flag"
	SourceEnv   Source = "environment"
	SourceFile  Source = "env-file"
	// SourceKeychain is the system keychain. It ranks after an explicitly
	// named env file and before the default one.
	SourceKeychain Source = "keychain"
)

// KeychainState says what resolution learned about the system keychain.
type KeychainState string

// Keychain states, as reported by `auth status` and `auth doctor`.
const (
	// KeychainFound means the keychain item exists and holds values.
	KeychainFound KeychainState = "found"
	// KeychainEmpty means the keychain was read and holds no pcenter item.
	KeychainEmpty KeychainState = "empty"
	// KeychainNotRead means flags, environment and an explicit file already
	// set everything, so the keychain was not consulted.
	KeychainNotRead KeychainState = "not-read"
	// KeychainBypassed means PCENTER_BYPASS_KEYCHAIN turned the keychain off.
	KeychainBypassed KeychainState = "bypassed"
	// KeychainUnavailable means the keychain could not be read.
	KeychainUnavailable KeychainState = "unavailable"
	// KeychainInvalid means the keychain item is not valid JSON.
	KeychainInvalid KeychainState = "invalid"
)

// keychainLabel names the keychain item in messages.
const keychainLabel = "system keychain (service " + KeychainService + ", account " + KeychainAccount + ")"

// Credential variable names, in the order they are shown to a user.
const (
	TenantIDName     = "MS_STORE_TENANT_ID"
	ClientIDName     = "MS_STORE_CLIENT_ID"
	ClientSecretName = "MS_STORE_CLIENT_SECRET"
	AppIDName        = "MS_STORE_APP_ID"
)

// AccountNames authenticate the account. AppID identifies which product a
// command acts on, so it is stored and reported separately: one account can
// publish several apps, and `auth login` must work before any app is chosen.
var AccountNames = []string{TenantIDName, ClientIDName, ClientSecretName}

// AllNames is AccountNames plus the app id.
var AllNames = []string{TenantIDName, ClientIDName, ClientSecretName, AppIDName}

// SecretNames must never be echoed back to a user.
var SecretNames = map[string]bool{ClientSecretName: true}

// Report is a resolution with its provenance, for `auth status` and
// `auth doctor`. Values are present so callers can act on them; use Redacted
// before rendering.
type Report struct {
	Config        Config
	EnvFile       string
	EnvFileExists bool
	// EnvFileExplicit records whether the path came from --env-file or
	// PCENTER_ENV_FILE rather than the default location.
	EnvFileExplicit bool
	EnvFileMode     fs.FileMode
	// EnvFileError is why a default env file that exists could not be read.
	// An explicitly named file fails Inspect instead.
	EnvFileError string
	// Sources maps each MS_STORE_* name to where its value came from.
	Sources map[string]Source
	// EnvFileNames and KeychainNames list the names each store sets, in
	// AllNames order, whichever source wins.
	EnvFileNames  []string
	KeychainNames []string
	// KeychainState and KeychainError describe the keychain lookup.
	KeychainState KeychainState
	KeychainError string
	// KeychainHasAny is true when the keychain item sets any of AllNames.
	KeychainHasAny bool
	// KeychainHasAccount and EnvFileHasAccount are true when that store alone
	// sets all of AccountNames.
	KeychainHasAccount bool
	EnvFileHasAccount  bool
}

// Missing lists the given names that resolved to nothing, in AllNames order.
func (r Report) Missing(names []string) []string {
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	missing := make([]string, 0, len(names))
	for _, name := range AllNames {
		if wanted[name] && r.Sources[name] == SourceUnset {
			missing = append(missing, name)
		}
	}
	return missing
}

// Value returns the resolved value for a credential name.
func (r Report) Value(name string) string {
	switch name {
	case TenantIDName:
		return r.Config.TenantID
	case ClientIDName:
		return r.Config.ClientID
	case ClientSecretName:
		return r.Config.ClientSecret
	case AppIDName:
		return r.Config.AppID
	}
	return ""
}

// WorldReadable reports whether the env file is readable beyond its owner,
// which matters because it holds a client secret in plain text.
func (r Report) WorldReadable() bool {
	return r.EnvFileExists && r.EnvFileMode.Perm()&0o077 != 0
}

// MissingConfigError explains not just what is missing but where pcenter
// looked and what to do about it. The bare list of variable names that
// preceded it told a first-time user nothing actionable.
type MissingConfigError struct {
	Missing         []string
	EnvFile         string
	EnvFileExists   bool
	EnvFileExplicit bool
	KeychainState   KeychainState
}

// EnvFileError is a credentials file named explicitly that could not be read.
// Distinct from MissingConfigError: the fix is to create that file (or correct
// the path), not to supply settings some other way.
type EnvFileError struct {
	Path   string
	Reason error
}

func (e *EnvFileError) Error() string {
	return fmt.Sprintf(
		"open env file %q: %v\n\ncreate it with \"pcenter auth login --env-file %s\", or set the MS_STORE_* environment variables",
		e.Path, e.Reason, e.Path)
}

// Unwrap keeps errors.Is(err, os.ErrNotExist) working for callers that check.
func (e *EnvFileError) Unwrap() error { return e.Reason }

// ErrorCode marks this as a setup problem rather than a runtime failure.
func (e *EnvFileError) ErrorCode() string { return output.CodeEnvFile }

// ErrorDetails names the file and the command that creates it.
func (e *EnvFileError) ErrorDetails() map[string]any {
	return map[string]any{
		"envFile": e.Path,
		"remedy":  "pcenter auth login --env-file " + e.Path,
	}
}

// ErrorCode identifies this as resolvable configuration rather than a failure
// the caller can do nothing about.
func (e *MissingConfigError) ErrorCode() string { return output.CodeMissingConfiguration }

// ErrorDetails lists what is missing and where it was sought, so automation can
// fix the setup without parsing the human explanation.
func (e *MissingConfigError) ErrorDetails() map[string]any {
	return map[string]any{
		"missing":       e.Missing,
		"envFile":       e.EnvFile,
		"envFileExists": e.EnvFileExists,
		"keychain":      string(e.KeychainState),
		"remedy":        "pcenter auth login",
	}
}

func (e *MissingConfigError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "missing required configuration: %s", strings.Join(e.Missing, ", "))

	state := "no such file"
	if e.EnvFileExists {
		state = "found, but does not set them"
	}
	keychain := string(e.KeychainState)
	if keychain == "" {
		keychain = string(KeychainUnavailable)
	}
	fileLine := fmt.Sprintf("%s (%s)", e.EnvFile, state)
	keychainLine := fmt.Sprintf("%s (%s)", keychainLabel, keychain)
	stores := []string{fileLine, keychainLine}
	if !e.EnvFileExplicit {
		stores = []string{keychainLine, fileLine}
	}
	fmt.Fprintf(&b, "\n\nchecked, in order:")
	fmt.Fprintf(&b, "\n  1. command-line flags")
	fmt.Fprintf(&b, "\n  2. MS_STORE_* environment variables")
	fmt.Fprintf(&b, "\n  3. %s", stores[0])
	fmt.Fprintf(&b, "\n  4. %s", stores[1])
	fmt.Fprintf(&b, "\n\nrun \"pcenter auth login\" to store credentials, or set the variables in the environment")
	return b.String()
}

// Inspect resolves credentials without requiring them, reporting where each
// value came from. `auth login` and `auth doctor` need to run when the
// configuration is incomplete, which Resolve by design refuses to do.
//
// A missing file is reported through EnvFileExists rather than returned as an
// error, even when the path was given explicitly: `auth login --env-file` names
// the file it is about to create, and `auth doctor` has to be able to say the
// file is absent. Resolve reapplies the stricter rule for operational commands.
// A malformed file is still an error — that is a real problem at any callsite.
func Inspect(overrides Overrides, environment Environment, keychain Keychain) (Report, error) {
	envFile, explicit, err := envFilePath(overrides, environment)
	if err != nil {
		return Report{}, err
	}

	report := Report{
		EnvFile:         envFile,
		EnvFileExplicit: explicit,
		Sources:         make(map[string]Source, len(AllNames)),
	}

	fileValues, err := parseEnvFile(envFile)
	report.EnvFileExists = true
	if err != nil {
		switch {
		case os.IsNotExist(underlying(err)):
			report.EnvFileExists = false
		case explicit:
			return Report{}, err
		default:
			// A broken default file must not hide credentials the keychain
			// holds; ValidateReport surfaces it if anything is still missing.
			report.EnvFileError = err.Error()
		}
		fileValues = Environment{}
	}
	if report.EnvFileExists {
		if info, statErr := os.Stat(envFile); statErr == nil {
			report.EnvFileMode = info.Mode()
		}
	}

	flagValues := map[string]string{
		TenantIDName:     overrides.TenantID,
		ClientIDName:     overrides.ClientID,
		ClientSecretName: overrides.ClientSecret,
		AppIDName:        overrides.AppID,
	}

	keychainValues := inspectKeychain(&report, keychain, environment, func(name string) bool {
		if flagValues[name] != "" || environment[name] != "" {
			return true
		}
		return explicit && fileValues[name] != ""
	})

	resolved := make(map[string]string, len(AllNames))
	for _, name := range AllNames {
		candidates := []candidate{
			{flagValues[name], SourceFlag},
			{environment[name], SourceEnv},
		}
		file := candidate{fileValues[name], SourceFile}
		stored := candidate{keychainValues[name], SourceKeychain}
		if explicit {
			candidates = append(candidates, file, stored)
		} else {
			candidates = append(candidates, stored, file)
		}
		value, source := pick(candidates...)
		resolved[name] = value
		report.Sources[name] = source
		if fileValues[name] != "" {
			report.EnvFileNames = append(report.EnvFileNames, name)
		}
		if keychainValues[name] != "" {
			report.KeychainNames = append(report.KeychainNames, name)
		}
	}
	report.KeychainHasAny = len(report.KeychainNames) > 0
	report.KeychainHasAccount = setsAll(keychainValues, AccountNames)
	report.EnvFileHasAccount = setsAll(fileValues, AccountNames)

	report.Config = Config{
		TenantID:     resolved[TenantIDName],
		ClientID:     resolved[ClientIDName],
		ClientSecret: resolved[ClientSecretName],
		AppID:        resolved[AppIDName],
		EnvFile:      envFile,
		APIBase:      firstNonEmpty(environment["PCENTER_API_BASE"], defaultAPIBase),
		LoginBase:    firstNonEmpty(environment["PCENTER_LOGIN_BASE"], defaultLoginBase),
	}
	return report, nil
}

// inspectKeychain records the keychain state on the report and returns the
// values to use. setEarlier reports whether a higher-ranked source already
// sets a name; when every name is set, the keychain is not read at all, so a
// CI job with every variable set never touches one.
func inspectKeychain(report *Report, keychain Keychain, environment Environment, setEarlier func(string) bool) map[string]string {
	if BypassKeychain(environment) {
		report.KeychainState = KeychainBypassed
		return nil
	}
	everything := true
	for _, name := range AllNames {
		if !setEarlier(name) {
			everything = false
			break
		}
	}
	if everything {
		report.KeychainState = KeychainNotRead
		return nil
	}
	if keychain == nil {
		report.KeychainState = KeychainUnavailable
		report.KeychainError = "no system keychain"
		return nil
	}
	values, err := LoadKeychain(keychain)
	var formatErr *KeychainFormatError
	switch {
	case errors.As(err, &formatErr):
		report.KeychainState = KeychainInvalid
		report.KeychainError = err.Error()
		return nil
	case err != nil:
		report.KeychainState = KeychainUnavailable
		report.KeychainError = err.Error()
		return nil
	case len(values) == 0:
		report.KeychainState = KeychainEmpty
	default:
		report.KeychainState = KeychainFound
	}
	return values
}

func setsAll(values map[string]string, names []string) bool {
	for _, name := range names {
		if values[name] == "" {
			return false
		}
	}
	return true
}

// Save merges values into the env file, creating it with owner-only
// permissions. Unlisted keys already in the file are preserved, so writing an
// app id later does not discard the account credentials.
func Save(path string, values map[string]string) error {
	existing, err := parseEnvFile(path)
	if err != nil {
		if !os.IsNotExist(underlying(err)) {
			return err
		}
		existing = Environment{}
	}
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		existing[key] = value
	}

	keys := make([]string, 0, len(existing))
	for key := range existing {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("# pcenter credentials. Written by \"pcenter auth login\".\n")
	b.WriteString("# Keep this file readable only by you; it holds a client secret.\n")
	for _, key := range keys {
		fmt.Fprintf(&b, "%s=%s\n", key, existing[key])
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", directory, err)
	}

	// Written to a temporary file and renamed so an interrupted write cannot
	// leave a half-populated credentials file behind.
	temp, err := os.CreateTemp(directory, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", directory, err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set permissions on %s: %w", tempName, err)
	}
	if _, err := temp.WriteString(b.String()); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write %s: %w", tempName, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tempName, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Remove deletes the credentials file. A missing file is not an error, so
// `auth logout` is safe to run twice.
func Remove(path string) (bool, error) {
	err := os.Remove(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("remove %s: %w", path, err)
}

// DefaultEnvFile is where credentials live when nothing overrides the path.
func DefaultEnvFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "pcenter", "credentials.env"), nil
}

// EnvFilePath returns the credentials file path without reading the file, for
// commands such as `auth logout` that must work when the file is unreadable.
func EnvFilePath(overrides Overrides, environment Environment) (string, error) {
	path, _, err := envFilePath(overrides, environment)
	return path, err
}

func envFilePath(overrides Overrides, environment Environment) (path string, explicit bool, err error) {
	defaultPath, err := DefaultEnvFile()
	if err != nil {
		return "", false, err
	}
	explicit = overrides.EnvFile != "" || environment["PCENTER_ENV_FILE"] != ""
	chosen := firstNonEmpty(overrides.EnvFile, environment["PCENTER_ENV_FILE"], defaultPath)
	expanded, err := expandHome(chosen)
	if err != nil {
		return "", false, err
	}
	return expanded, explicit, nil
}

// expandHome resolves a leading ~ so a quoted --env-file, a CI YAML value or a
// documented PCENTER_ENV_FILE=~/... does not silently resolve to a literal "~"
// directory that will never exist. Only the current user's home is handled;
// ~otheruser is left alone rather than guessed at.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", path, err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

type candidate struct {
	value  string
	source Source
}

// pick returns the first non-empty candidate, in precedence order.
func pick(candidates ...candidate) (string, Source) {
	for _, c := range candidates {
		if c.value != "" {
			return c.value, c.source
		}
	}
	return "", SourceUnset
}

// underlying unwraps the fmt.Errorf wrapping parseEnvFile applies, so callers
// can still test for os.ErrNotExist.
func underlying(err error) error {
	type unwrapper interface{ Unwrap() error }
	for err != nil {
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
			continue
		}
		return err
	}
	return err
}
