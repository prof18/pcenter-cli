package config_test

import (
	"errors"
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
		nil,
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

	report, err := config.Inspect(config.Overrides{EnvFile: envFile}, config.Environment{}, nil)
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
	if _, err := config.Inspect(config.Overrides{EnvFile: envFile}, config.Environment{}, nil); err == nil {
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

	report, err := config.Inspect(config.Overrides{EnvFile: envFile}, config.Environment{}, nil)
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
	report, err := config.Inspect(config.Overrides{EnvFile: envFile}, config.Environment{}, nil)
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
	for _, expected := range []string{config.ClientIDName, "/somewhere/credentials.env", "auth login", "no such file", "keychain"} {
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
	report, err := config.Inspect(config.Overrides{EnvFile: "~/creds.env"}, config.Environment{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.EnvFileExists || report.Config.TenantID != "tenant" {
		t.Fatalf("tilde was not expanded: file=%q exists=%v", report.EnvFile, report.EnvFileExists)
	}
}

func seededKeychain(t *testing.T, values map[string]string) *config.MemoryKeychain {
	t.Helper()
	keychain := config.NewMemoryKeychain()
	if err := config.SaveKeychain(keychain, values); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}
	return keychain
}

func allKeychainValues() map[string]string {
	return map[string]string{
		config.TenantIDName:     "kc-tenant",
		config.ClientIDName:     "kc-client",
		config.ClientSecretName: "kc-secret",
		config.AppIDName:        "kc-app",
	}
}

func TestInspectKeychainFillsWhatTheExplicitFileLacks(t *testing.T) {
	t.Parallel()
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	if err := os.WriteFile(envFile, []byte("MS_STORE_CLIENT_SECRET=file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := config.Inspect(config.Overrides{EnvFile: envFile}, config.Environment{}, seededKeychain(t, allKeychainValues()))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]config.Source{
		config.TenantIDName:     config.SourceKeychain,
		config.ClientIDName:     config.SourceKeychain,
		config.ClientSecretName: config.SourceFile,
		config.AppIDName:        config.SourceKeychain,
	} {
		if got := report.Sources[name]; got != want {
			t.Fatalf("%s source = %q, want %q", name, got, want)
		}
	}
	if report.Config.ClientSecret != "file-secret" {
		t.Fatalf("explicit file should beat the keychain, got %q", report.Config.ClientSecret)
	}
}

func TestInspectKeychainBeatsTheDefaultFile(t *testing.T) {
	// Not parallel: the default credentials path is derived from HOME.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	defaultFile, err := config.DefaultEnvFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(defaultFile, map[string]string{
		config.TenantIDName: "file-tenant", config.ClientIDName: "file-client",
		config.ClientSecretName: "file-secret", config.AppIDName: "file-app",
	}); err != nil {
		t.Fatal(err)
	}
	report, err := config.Inspect(config.Overrides{}, config.Environment{}, seededKeychain(t, allKeychainValues()))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range config.AllNames {
		if report.Sources[name] != config.SourceKeychain {
			t.Fatalf("%s source = %q, want keychain", name, report.Sources[name])
		}
	}
	if !report.KeychainHasAccount || !report.EnvFileHasAccount || report.KeychainState != config.KeychainFound {
		t.Fatalf("report flags wrong: %+v", report)
	}
}

func TestInspectSkipsTheKeychainWhenEverythingIsSet(t *testing.T) {
	t.Parallel()
	keychain := config.NewMemoryKeychain()
	keychain.Err = errors.New("must not be read")
	report, err := config.Inspect(config.Overrides{EnvFile: filepath.Join(t.TempDir(), "absent.env")}, config.Environment{
		"MS_STORE_TENANT_ID": "t", "MS_STORE_CLIENT_ID": "c", "MS_STORE_CLIENT_SECRET": "s", "MS_STORE_APP_ID": "a",
	}, keychain)
	if err != nil {
		t.Fatal(err)
	}
	if report.KeychainState != config.KeychainNotRead {
		t.Fatalf("state = %q, want not-read", report.KeychainState)
	}
}

func TestInspectToleratesAnUnavailableKeychain(t *testing.T) {
	t.Parallel()
	keychain := config.NewMemoryKeychain()
	keychain.Err = errors.New("locked")
	report, err := config.Inspect(config.Overrides{EnvFile: filepath.Join(t.TempDir(), "absent.env")}, config.Environment{}, keychain)
	if err != nil {
		t.Fatal(err)
	}
	if report.KeychainState != config.KeychainUnavailable || report.KeychainError == "" {
		t.Fatalf("state = %q error = %q", report.KeychainState, report.KeychainError)
	}
}

func TestInspectBypassSkipsTheKeychain(t *testing.T) {
	t.Parallel()
	report, err := config.Inspect(
		config.Overrides{EnvFile: filepath.Join(t.TempDir(), "absent.env")},
		config.Environment{config.BypassKeychainEnv: "1"},
		seededKeychain(t, allKeychainValues()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.KeychainState != config.KeychainBypassed || report.Sources[config.TenantIDName] != config.SourceUnset {
		t.Fatalf("state = %q tenant source = %q", report.KeychainState, report.Sources[config.TenantIDName])
	}
}

func TestInspectUsesTheKeychainWhenTheDefaultFileIsMalformed(t *testing.T) {
	// Not parallel: the default credentials path is derived from HOME.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	defaultFile, err := config.DefaultEnvFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(defaultFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultFile, []byte("not an env line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := config.Inspect(config.Overrides{}, config.Environment{}, seededKeychain(t, allKeychainValues()))
	if err != nil {
		t.Fatalf("a malformed default file must not fail Inspect: %v", err)
	}
	if report.EnvFileError == "" {
		t.Fatal("EnvFileError should record the parse problem")
	}
	for _, name := range config.AllNames {
		if report.Sources[name] != config.SourceKeychain {
			t.Fatalf("%s source = %q, want keychain", name, report.Sources[name])
		}
	}
	_, err = config.Resolve(config.Overrides{}, config.Environment{}, config.NewMemoryKeychain())
	if err == nil || !strings.Contains(err.Error(), "expected KEY=VALUE") {
		t.Fatalf("Resolve with an empty keychain should surface the parse error, got %v", err)
	}
}

func TestInspectIgnoresAMalformedKeychainItem(t *testing.T) {
	t.Parallel()
	keychain := config.NewMemoryKeychain()
	if err := keychain.Set(config.KeychainService, config.KeychainAccount, "not json"); err != nil {
		t.Fatal(err)
	}
	report, err := config.Inspect(config.Overrides{EnvFile: filepath.Join(t.TempDir(), "absent.env")}, config.Environment{}, keychain)
	if err != nil {
		t.Fatalf("a malformed keychain item must not fail Inspect: %v", err)
	}
	if report.KeychainState != config.KeychainInvalid || report.KeychainError == "" {
		t.Fatalf("state = %q error = %q", report.KeychainState, report.KeychainError)
	}
	for _, name := range config.AllNames {
		if report.Sources[name] == config.SourceKeychain {
			t.Fatalf("%s should not come from an invalid item", name)
		}
	}
}
