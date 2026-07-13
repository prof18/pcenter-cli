package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/prof18/pcenter-cli/internal/config"
)

func TestResolvePrecedenceFlagThenEnvironmentThenFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	envFile := filepath.Join(dir, "credentials.env")
	contents := []byte("MS_STORE_TENANT_ID=file-tenant\nMS_STORE_CLIENT_ID=file-client\nMS_STORE_CLIENT_SECRET=file-secret\nMS_STORE_APP_ID=file-app\n")
	if err := os.WriteFile(envFile, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := config.Resolve(config.Overrides{TenantID: "flag-tenant", EnvFile: envFile}, config.Environment{
		"MS_STORE_CLIENT_ID":     "env-client",
		"MS_STORE_CLIENT_SECRET": "env-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.TenantID != "flag-tenant" || resolved.ClientID != "env-client" || resolved.ClientSecret != "env-secret" || resolved.AppID != "file-app" {
		t.Fatalf("unexpected config: %+v", resolved.Redacted())
	}
}

func TestResolveEnvFilePathPrecedence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "custom.env")
	if err := os.WriteFile(file, []byte("MS_STORE_TENANT_ID=t\nMS_STORE_CLIENT_ID=c\nMS_STORE_CLIENT_SECRET=s\nMS_STORE_APP_ID=a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := config.Resolve(config.Overrides{}, config.Environment{"PCENTER_ENV_FILE": file})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.EnvFile != file {
		t.Fatalf("env file = %q, want %q", resolved.EnvFile, file)
	}
}

func TestResolveStripsMatchingShellStyleQuotesFromEnvFileValues(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "quoted.env")
	contents := []byte("MS_STORE_TENANT_ID=\"tenant\"\nMS_STORE_CLIENT_ID='client'\nMS_STORE_CLIENT_SECRET=\"secret=with=equals\"\nMS_STORE_APP_ID='app'\n")
	if err := os.WriteFile(file, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Resolve(config.Overrides{EnvFile: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.TenantID != "tenant" || resolved.ClientID != "client" || resolved.ClientSecret != "secret=with=equals" || resolved.AppID != "app" {
		t.Fatalf("quoted values were not normalized: %+v", resolved.Redacted())
	}
}

func TestResolveAllowsMissingDefaultFileWhenEnvironmentIsComplete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resolved, err := config.Resolve(config.Overrides{}, config.Environment{
		"MS_STORE_TENANT_ID":     "t",
		"MS_STORE_CLIENT_ID":     "c",
		"MS_STORE_CLIENT_SECRET": "s",
		"MS_STORE_APP_ID":        "a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AppID != "a" {
		t.Fatalf("app id = %q", resolved.AppID)
	}
}

func TestResolveRejectsMissingValuesAndMalformedFile(t *testing.T) {
	t.Parallel()
	_, err := config.Resolve(config.Overrides{EnvFile: filepath.Join(t.TempDir(), "missing.env")}, nil)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit missing file error = %v", err)
	}

	file := filepath.Join(t.TempDir(), "bad.env")
	if writeErr := os.WriteFile(file, []byte("NOT-A-PAIR\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	_, err = config.Resolve(config.Overrides{EnvFile: file}, nil)
	if err == nil {
		t.Fatal("malformed env file unexpectedly accepted")
	}
}

func TestRedactedNeverExposesClientSecret(t *testing.T) {
	t.Parallel()
	cfg := config.Config{TenantID: "t", ClientID: "c", ClientSecret: "very-secret", AppID: "a"}
	redacted := cfg.Redacted()
	if redacted.ClientSecret == cfg.ClientSecret || redacted.ClientSecret == "" {
		t.Fatalf("secret was not redacted: %+v", redacted)
	}
}
