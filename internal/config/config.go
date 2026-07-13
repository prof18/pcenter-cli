// Package config resolves Partner Center credentials without persisting secrets.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve home directory: %w", err)
	}
	defaultEnvFile := filepath.Join(home, ".config", "pcenter", "credentials.env")
	envFile := firstNonEmpty(overrides.EnvFile, environment["PCENTER_ENV_FILE"], defaultEnvFile)
	explicitEnvFile := overrides.EnvFile != "" || environment["PCENTER_ENV_FILE"] != ""

	fileValues, err := parseEnvFile(envFile)
	if err != nil {
		if explicitEnvFile || !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
		fileValues = Environment{}
	}

	config := Config{
		TenantID:     firstNonEmpty(overrides.TenantID, environment["MS_STORE_TENANT_ID"], fileValues["MS_STORE_TENANT_ID"]),
		ClientID:     firstNonEmpty(overrides.ClientID, environment["MS_STORE_CLIENT_ID"], fileValues["MS_STORE_CLIENT_ID"]),
		ClientSecret: firstNonEmpty(overrides.ClientSecret, environment["MS_STORE_CLIENT_SECRET"], fileValues["MS_STORE_CLIENT_SECRET"]),
		AppID:        firstNonEmpty(overrides.AppID, environment["MS_STORE_APP_ID"], fileValues["MS_STORE_APP_ID"]),
		EnvFile:      envFile,
		APIBase:      firstNonEmpty(environment["PCENTER_API_BASE"], defaultAPIBase),
		LoginBase:    firstNonEmpty(environment["PCENTER_LOGIN_BASE"], defaultLoginBase),
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate reports all missing credential names without revealing values.
func (c Config) Validate() error {
	missing := make([]string, 0, 4)
	for name, value := range map[string]string{
		"MS_STORE_TENANT_ID":     c.TenantID,
		"MS_STORE_CLIENT_ID":     c.ClientID,
		"MS_STORE_CLIENT_SECRET": c.ClientSecret,
		"MS_STORE_APP_ID":        c.AppID,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
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
