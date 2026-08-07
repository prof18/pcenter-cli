package config

import (
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
)

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
	// Sources maps each MS_STORE_* name to where its value came from.
	Sources map[string]Source
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
	Missing       []string
	EnvFile       string
	EnvFileExists bool
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
	fmt.Fprintf(&b, "\n\nchecked, in order:")
	fmt.Fprintf(&b, "\n  1. command-line flags")
	fmt.Fprintf(&b, "\n  2. MS_STORE_* environment variables")
	fmt.Fprintf(&b, "\n  3. %s (%s)", e.EnvFile, state)
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
func Inspect(overrides Overrides, environment Environment) (Report, error) {
	envFile, explicit, err := envFilePath(overrides, environment)
	if err != nil {
		return Report{}, err
	}

	fileValues, err := parseEnvFile(envFile)
	exists := true
	if err != nil {
		if !os.IsNotExist(underlying(err)) {
			return Report{}, err
		}
		fileValues = Environment{}
		exists = false
	}

	var mode fs.FileMode
	if exists {
		if info, statErr := os.Stat(envFile); statErr == nil {
			mode = info.Mode()
		}
	}

	report := Report{
		EnvFile:         envFile,
		EnvFileExists:   exists,
		EnvFileExplicit: explicit,
		EnvFileMode:     mode,
		Sources:         make(map[string]Source, len(AllNames)),
	}
	flagValues := map[string]string{
		TenantIDName:     overrides.TenantID,
		ClientIDName:     overrides.ClientID,
		ClientSecretName: overrides.ClientSecret,
		AppIDName:        overrides.AppID,
	}
	resolved := make(map[string]string, len(AllNames))
	for _, name := range AllNames {
		value, source := pick(flagValues[name], environment[name], fileValues[name])
		resolved[name] = value
		report.Sources[name] = source
	}

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

func pick(flagValue, envValue, fileValue string) (string, Source) {
	switch {
	case flagValue != "":
		return flagValue, SourceFlag
	case envValue != "":
		return envValue, SourceEnv
	case fileValue != "":
		return fileValue, SourceFile
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
