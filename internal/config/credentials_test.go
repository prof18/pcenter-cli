package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/config"
)

func TestInspectReportsWhereEachValueCameFrom(t *testing.T) {
	t.Parallel()
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	contents := "MS_STORE_CLIENT_SECRET=file-secret\nMS_STORE_APP_ID=file-app\n"
	if err := os.WriteFile(envFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := config.Inspect(
		config.Overrides{TenantID: "flag-tenant", EnvFile: envFile},
		config.Environment{"MS_STORE_CLIENT_ID": "env-client"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]config.Source{
		config.TenantIDName:     config.SourceFlag,
		config.ClientIDName:     config.SourceEnv,
		config.ClientSecretName: config.SourceFile,
		config.AppIDName:        config.SourceFile,
	} {
		if got := report.Sources[name]; got != want {
			t.Fatalf("%s source = %q, want %q", name, got, want)
		}
	}
	if !report.EnvFileExists || !report.EnvFileExplicit {
		t.Fatalf("env file flags wrong: exists=%v explicit=%v", report.EnvFileExists, report.EnvFileExplicit)
	}
}

func TestInspectToleratesAMissingFileSoLoginCanCreateIt(t *testing.T) {
	t.Parallel()
	envFile := filepath.Join(t.TempDir(), "absent.env")

	report, err := config.Inspect(config.Overrides{EnvFile: envFile}, config.Environment{})
	if err != nil {
		t.Fatalf("Inspect must not fail on a missing file: %v", err)
	}
	if report.EnvFileExists {
		t.Fatal("EnvFileExists should be false")
	}
	if missing := report.Missing(config.AccountNames); len(missing) != len(config.AccountNames) {
		t.Fatalf("missing = %v, want all account names", missing)
	}
}

func TestInspectStillFailsOnAMalformedFile(t *testing.T) {
	t.Parallel()
	envFile := filepath.Join(t.TempDir(), "broken.env")
	if err := os.WriteFile(envFile, []byte("this is not a key value line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Inspect(config.Overrides{EnvFile: envFile}, config.Environment{}); err == nil {
		t.Fatal("a malformed file is a real problem and must be reported")
	}
}

func TestSaveMergesAndKeepsTheFileOwnerOnly(t *testing.T) {
	t.Parallel()
	envFile := filepath.Join(t.TempDir(), "sub", "credentials.env")

	if err := config.Save(envFile, map[string]string{
		config.TenantIDName:     "tenant",
		config.ClientSecretName: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	// A later write of one key must not discard the others.
	if err := config.Save(envFile, map[string]string{config.AppIDName: "app"}); err != nil {
		t.Fatal(err)
	}

	report, err := config.Inspect(config.Overrides{EnvFile: envFile}, config.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Config.TenantID != "tenant" || report.Config.ClientSecret != "secret" || report.Config.AppID != "app" {
		t.Fatalf("merge lost a value: %+v", report.Config.Redacted())
	}

	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(envFile)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("mode = %04o; the file holds a client secret", perm)
		}
	}
}

func TestSaveIgnoresBlankValues(t *testing.T) {
	t.Parallel()
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	if err := config.Save(envFile, map[string]string{config.TenantIDName: "tenant"}); err != nil {
		t.Fatal(err)
	}
	// An empty flag value must not blank out a stored credential.
	if err := config.Save(envFile, map[string]string{config.TenantIDName: "", config.ClientIDName: "client"}); err != nil {
		t.Fatal(err)
	}
	report, err := config.Inspect(config.Overrides{EnvFile: envFile}, config.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Config.TenantID != "tenant" || report.Config.ClientID != "client" {
		t.Fatalf("blank overwrote a stored value: %+v", report.Config.Redacted())
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	t.Parallel()
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	if err := config.Save(envFile, map[string]string{config.TenantIDName: "tenant"}); err != nil {
		t.Fatal(err)
	}
	removed, err := config.Remove(envFile)
	if err != nil || !removed {
		t.Fatalf("first remove: removed=%v err=%v", removed, err)
	}
	removed, err = config.Remove(envFile)
	if err != nil || removed {
		t.Fatalf("second remove should be a no-op: removed=%v err=%v", removed, err)
	}
}

func TestMissingConfigErrorNamesTheFileAndTheFix(t *testing.T) {
	t.Parallel()
	err := &config.MissingConfigError{
		Missing: []string{config.ClientIDName},
		EnvFile: "/somewhere/credentials.env",
	}
	message := err.Error()
	for _, expected := range []string{config.ClientIDName, "/somewhere/credentials.env", "auth login", "no such file"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message missing %q: %s", expected, message)
		}
	}
}

func TestEnvFilePathExpandsLeadingTilde(t *testing.T) {
	// Not parallel: resolution reads HOME.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	envFile := filepath.Join(home, "creds.env")
	if err := config.Save(envFile, map[string]string{config.TenantIDName: "tenant"}); err != nil {
		t.Fatal(err)
	}

	// A quoted flag, a CI YAML value or a documented PCENTER_ENV_FILE keeps the
	// literal tilde; without expansion this silently finds nothing.
	report, err := config.Inspect(config.Overrides{EnvFile: "~/creds.env"}, config.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.EnvFileExists || report.Config.TenantID != "tenant" {
		t.Fatalf("tilde was not expanded: file=%q exists=%v", report.EnvFile, report.EnvFileExists)
	}
}
