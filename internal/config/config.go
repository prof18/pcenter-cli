// Package config resolves Partner Center credentials without persisting secrets.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	defaultAPIBase   = "https://manage.devcenter.microsoft.com/v1.0/my"
	defaultLoginBase = "https://login.microsoftonline.com"
)

// Environment is an explicit environment snapshot, which keeps resolution deterministic in tests.
type Environment map[string]string

// Overrides are values supplied by command-line flags.
type Overrides struct {
	TenantID     string
	ClientID     string
	ClientSecret string
	AppID        string
	EnvFile      string
}

// Config contains resolved credentials and endpoint settings.
type Config struct {
	TenantID     string
	ClientID     string
	ClientSecret string
	AppID        string
	EnvFile      string
	APIBase      string
	LoginBase    string
}

// CurrentEnvironment snapshots the process environment.
func CurrentEnvironment() Environment {
	result := make(Environment)
	for _, pair := range os.Environ() {
		key, value, ok := strings.Cut(pair, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

// Resolve applies flag, environment, then env-file precedence and validates credentials.
func Resolve(overrides Overrides, environment Environment) (Config, error) {
	report, err := Inspect(overrides, environment)
	if err != nil {
		return Config{}, err
	}
	// An operational command told to read a specific file should say so when
	// that file is not there, rather than silently falling back and failing
	// later on a missing credential.
	if report.EnvFileExplicit && !report.EnvFileExists {
		return Config{}, &EnvFileError{Path: report.EnvFile, Reason: os.ErrNotExist}
	}
	if missing := report.Missing(AllNames); len(missing) > 0 {
		return Config{}, &MissingConfigError{
			Missing:       missing,
			EnvFile:       report.EnvFile,
			EnvFileExists: report.EnvFileExists,
		}
	}
	return report.Config, nil
}

// Validate reports all missing credential names without revealing values.
func (c Config) Validate() error {
	missing := make([]string, 0, len(AllNames))
	values := map[string]string{
		TenantIDName:     c.TenantID,
		ClientIDName:     c.ClientID,
		ClientSecretName: c.ClientSecret,
		AppIDName:        c.AppID,
	}
	for _, name := range AllNames {
		if strings.TrimSpace(values[name]) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return &MissingConfigError{Missing: missing, EnvFile: c.EnvFile}
	}
	return nil
}

// Redacted returns a safe copy for diagnostics.
func (c Config) Redacted() Config {
	if c.ClientSecret != "" {
		c.ClientSecret = "[REDACTED]"
	}
	return c
}

func parseEnvFile(path string) (Environment, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open env file %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	values := make(Environment)
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("parse env file %q line %d: expected KEY=VALUE", path, lineNumber)
		}
		normalized, normalizeErr := normalizeEnvValue(strings.TrimSpace(value))
		if normalizeErr != nil {
			return nil, fmt.Errorf("parse env file %q line %d: %w", path, lineNumber, normalizeErr)
		}
		values[key] = normalized
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read env file %q: %w", path, err)
	}
	return values, nil
}

func normalizeEnvValue(value string) (string, error) {
	if len(value) == 0 {
		return value, nil
	}
	if value[0] != '\'' && value[0] != '"' {
		return value, nil
	}
	if len(value) < 2 || value[len(value)-1] != value[0] {
		return "", errors.New("unterminated quoted value")
	}
	return value[1 : len(value)-1], nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
