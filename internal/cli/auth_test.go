package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prof18/pcenter-cli/internal/cli"
	"github.com/prof18/pcenter-cli/internal/config"
	"github.com/prof18/pcenter-cli/internal/fakestore"
	"github.com/prof18/pcenter-cli/internal/output"
)

func TestAuthLoginWritesCredentialsFromFlags(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	envFile := filepath.Join(t.TempDir(), "nested", "credentials.env")
	environment := config.Environment{
		"PCENTER_API_BASE":   server.APIBase(),
		"PCENTER_LOGIN_BASE": server.LoginBase(),
	}

	stdout, stderr, exitCode := execute(t, environment, []string{
		"--output", "json", "--env-file", envFile, "auth", "login",
		"--tenant-id", "tenant", "--client-id", "client", "--client-secret", "secret",
	}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess || stderr != "" {
		t.Fatalf("exit = %d stderr = %q stdout = %q", exitCode, stderr, stdout)
	}

	contents, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	for _, expected := range []string{"MS_STORE_TENANT_ID=tenant", "MS_STORE_CLIENT_ID=client", "MS_STORE_CLIENT_SECRET=secret"} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("credentials missing %q: %s", expected, contents)
		}
	}
	// The app id is deliberately not stored unless asked for: one account can
	// publish several apps.
	if strings.Contains(string(contents), "MS_STORE_APP_ID") {
		t.Fatalf("app id should not be stored without --app-id: %s", contents)
	}
	// The secret must never be echoed back, even on success.
	if strings.Contains(stdout, "secret") {
		t.Fatalf("stdout leaked the client secret: %s", stdout)
	}
	assertOwnerOnly(t, envFile)
}

func TestAuthLoginRejectsCredentialsTheStoreRefuses(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{AppID: "APP", App: fakestore.App{ID: "APP", PrimaryName: "Example"}, RejectToken: true})
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	environment := config.Environment{
		"PCENTER_API_BASE":   server.APIBase(),
		"PCENTER_LOGIN_BASE": server.LoginBase(),
	}

	_, stderr, exitCode := execute(t, environment, []string{
		"--env-file", envFile, "auth", "login",
		"--tenant-id", "tenant", "--client-id", "client", "--client-secret", "wrong",
	}, cli.BuildInfo{})
	// Rejected credentials exit 3, not the generic 1, so automation can tell
	// "fix the credentials" from "the Store had a problem".
	if exitCode != output.ExitAuth {
		t.Fatalf("exit = %d, want %d (stderr %q)", exitCode, output.ExitAuth, stderr)
	}
	if !strings.Contains(stderr, "were not saved") {
		t.Fatalf("stderr should say nothing was written: %q", stderr)
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Fatalf("bad credentials must not be written; stat err = %v", err)
	}
}

func TestAuthLoginPreservesExistingValuesAndAddsAppID(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	environment := config.Environment{
		"PCENTER_API_BASE":   server.APIBase(),
		"PCENTER_LOGIN_BASE": server.LoginBase(),
	}

	_, _, exitCode := execute(t, environment, []string{
		"--env-file", envFile, "auth", "login",
		"--tenant-id", "tenant", "--client-id", "client", "--client-secret", "secret",
	}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess {
		t.Fatalf("first login exit = %d", exitCode)
	}

	// A second login supplying only the app id must not discard the account
	// credentials already in the file.
	stdout, stderr, exitCode := execute(t, environment, []string{
		"--output", "json", "--env-file", envFile, "auth", "login", "--app-id", "APP",
	}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess || stderr != "" {
		t.Fatalf("second login exit = %d stderr = %q stdout = %q", exitCode, stderr, stdout)
	}

	contents, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	for _, expected := range []string{"MS_STORE_TENANT_ID=tenant", "MS_STORE_CLIENT_SECRET=secret", "MS_STORE_APP_ID=APP"} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("credentials missing %q after second login: %s", expected, contents)
		}
	}
}

func TestAuthLoginPromptsOnlyWhenAttachedToTerminal(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	environment := config.Environment{
		"PCENTER_API_BASE":   server.APIBase(),
		"PCENTER_LOGIN_BASE": server.LoginBase(),
	}

	// Without a terminal, a missing value is an error naming the flag to pass —
	// never a prompt, which would hang a CI job forever.
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	_, stderr, exitCode := execute(t, environment, []string{"--env-file", envFile, "auth", "login"}, cli.BuildInfo{})
	if exitCode != output.ExitUsage {
		t.Fatalf("non-tty exit = %d, want %d", exitCode, output.ExitUsage)
	}
	if !strings.Contains(stderr, "--tenant-id") {
		t.Fatalf("error should name the flag to pass: %q", stderr)
	}

	// With a terminal, values are prompted for, and the secret goes through the
	// no-echo path.
	promptEnvFile := filepath.Join(t.TempDir(), "credentials.env")
	var asked, askedSecret []string
	var stdout, stderrBuf bytes.Buffer
	exitCode = cli.Execute(context.Background(), []string{"--env-file", promptEnvFile, "auth", "login"}, cli.Dependencies{
		Stdout:      &stdout,
		Stderr:      &stderrBuf,
		Environment: environment,
		IsTTY:       true,
		Now:         func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) },
		Clock:       instantClock{},
		Rand:        cliFixedRand(1),
		PromptLine: func(prompt string) (string, error) {
			asked = append(asked, prompt)
			return "typed-" + strings.TrimSuffix(strings.TrimSpace(prompt), ":"), nil
		},
		PromptSecret: func(prompt string) (string, error) {
			askedSecret = append(askedSecret, prompt)
			return "typed-secret", nil
		},
	})
	if exitCode != output.ExitSuccess {
		t.Fatalf("tty login exit = %d stderr = %q", exitCode, stderrBuf.String())
	}
	if len(askedSecret) != 1 || !strings.Contains(askedSecret[0], config.ClientSecretName) {
		t.Fatalf("client secret must be read without echo, got %v", askedSecret)
	}
	if len(asked) != 2 {
		t.Fatalf("tenant and client id should be prompted visibly, got %v", asked)
	}
	contents, err := os.ReadFile(promptEnvFile)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if !strings.Contains(string(contents), "MS_STORE_CLIENT_SECRET=typed-secret") {
		t.Fatalf("prompted secret not stored: %s", contents)
	}
}

func TestAuthDoctorReportsSourcesAndFailsWhenIncomplete(t *testing.T) {
	t.Parallel()
	server := fullServer(t)

	// Complete configuration: every check passes and the app is reachable.
	stdout, _, exitCode := execute(t, fakeEnvironment(server), []string{"--output", "json", "auth", "doctor"}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess {
		t.Fatalf("healthy doctor exit = %d: %s", exitCode, stdout)
	}
	var report struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Check  string `json:"check"`
			Status string `json:"status"`
			Detail string `json:"detail"`
			Source string `json:"source"`
			Value  string `json:"value"`
			Remedy string `json:"remedy"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor output is not JSON: %s", stdout)
	}
	if !report.OK {
		t.Fatalf("healthy setup should report ok=true: %s", stdout)
	}
	checks := report.Checks
	statuses := map[string]string{}
	for _, check := range checks {
		statuses[check.Check] = check.Status
		// The secret must never appear in a value field or the prose.
		if check.Check == config.ClientSecretName && (check.Value != "" || strings.Contains(check.Detail, "secret")) {
			t.Fatalf("doctor leaked the client secret: %+v", check)
		}
		// Every passing setting says where it came from as a field, not prose.
		if check.Status == "ok" && check.Check == config.TenantIDName && check.Source == "" {
			t.Fatalf("source must be a field, not buried in detail: %+v", check)
		}
	}
	for _, name := range []string{"token", "app", config.TenantIDName} {
		if statuses[name] != "ok" {
			t.Fatalf("%s status = %q, want ok: %s", name, statuses[name], stdout)
		}
	}

	// Missing credentials: doctor still reports rather than refusing to run,
	// but exits non-zero so CI can use it as a preflight.
	partial := config.Environment{
		"MS_STORE_TENANT_ID": "tenant",
		"PCENTER_API_BASE":   server.APIBase(),
		"PCENTER_LOGIN_BASE": server.LoginBase(),
		"PCENTER_ENV_FILE":   filepath.Join(t.TempDir(), "absent.env"),
	}
	stdout, _, exitCode = execute(t, partial, []string{"--output", "json", "auth", "doctor"}, cli.BuildInfo{})
	if exitCode != output.ExitFailure {
		t.Fatalf("incomplete doctor exit = %d, want %d: %s", exitCode, output.ExitFailure, stdout)
	}
	if !strings.Contains(stdout, config.ClientIDName) {
		t.Fatalf("doctor should name the missing setting: %s", stdout)
	}
	// A failing check carries the fix as a field so an agent can act on it.
	if !strings.Contains(stdout, `"ok":false`) || !strings.Contains(stdout, `"remedy":"pcenter auth login"`) {
		t.Fatalf("doctor should report ok=false with a remedy: %s", stdout)
	}
}

func TestAuthLogoutRequiresConfirmation(t *testing.T) {
	t.Parallel()
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	if err := config.Save(envFile, map[string]string{config.TenantIDName: "tenant"}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	environment := config.Environment{}

	_, stderr, exitCode := execute(t, environment, []string{"--env-file", envFile, "auth", "logout"}, cli.BuildInfo{})
	if exitCode != output.ExitUsage || !strings.Contains(stderr, "--yes") {
		t.Fatalf("logout without --yes: exit = %d stderr = %q", exitCode, stderr)
	}
	if _, err := os.Stat(envFile); err != nil {
		t.Fatalf("credentials must survive an unconfirmed logout: %v", err)
	}

	_, stderr, exitCode = execute(t, environment, []string{"--env-file", envFile, "auth", "logout", "--yes"}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess || stderr != "" {
		t.Fatalf("logout exit = %d stderr = %q", exitCode, stderr)
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Fatalf("credentials should be gone, stat err = %v", err)
	}

	// Running it twice is not an error.
	if _, _, exitCode = execute(t, environment, []string{"--env-file", envFile, "auth", "logout", "--yes"}, cli.BuildInfo{}); exitCode != output.ExitSuccess {
		t.Fatalf("second logout exit = %d", exitCode)
	}
}

func TestMissingCredentialsErrorExplainsWhereToLook(t *testing.T) {
	// Not parallel: the default credentials path is derived from HOME.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows equivalent

	// Deliberately not through execute(), which pins PCENTER_ENV_FILE to keep
	// tests off the real credentials file — this is the one test that must
	// exercise default path resolution, so it drives Execute directly against a
	// temporary HOME.
	var stdout, stderrBuf bytes.Buffer
	exitCode := cli.Execute(context.Background(), []string{"--output", "table", "app", "info"}, cli.Dependencies{
		Stdout:      &stdout,
		Stderr:      &stderrBuf,
		Environment: config.Environment{},
		Now:         func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) },
		Clock:       instantClock{},
		Rand:        cliFixedRand(1),
	})
	stderr := stderrBuf.String()
	if exitCode != output.ExitUsage {
		t.Fatalf("exit = %d, want %d (stderr %q)", exitCode, output.ExitUsage, stderr)
	}
	// Table mode carries the human explanation: what was missing, where pcenter
	// looked, and the command that fixes it.
	for _, expected := range []string{"credentials.env", "auth login", "environment variables", config.ClientSecretName} {
		if !strings.Contains(stderr, expected) {
			t.Fatalf("table error should mention %q: %s", expected, stderr)
		}
	}

	// JSON mode carries the same facts as fields instead of prose, so an agent
	// never has to parse the explanation.
	var jsonOut, jsonErr bytes.Buffer
	cli.Execute(context.Background(), []string{"--output", "json", "app", "info"}, cli.Dependencies{
		Stdout: &jsonOut, Stderr: &jsonErr, Environment: config.Environment{},
		Now:   func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) },
		Clock: instantClock{}, Rand: cliFixedRand(1),
	})
	var payload struct {
		Error struct {
			Code    string   `json:"code"`
			Message string   `json:"message"`
			Missing []string `json:"missing"`
			EnvFile string   `json:"envFile"`
			Remedy  string   `json:"remedy"`
		} `json:"error"`
	}
	if err := json.Unmarshal(jsonErr.Bytes(), &payload); err != nil {
		t.Fatalf("json error is not parseable: %s", jsonErr.String())
	}
	if payload.Error.Code != output.CodeMissingConfiguration {
		t.Fatalf("code = %q", payload.Error.Code)
	}
	if len(payload.Error.Missing) != 4 || payload.Error.Remedy == "" || payload.Error.EnvFile == "" {
		t.Fatalf("structured fields incomplete: %+v", payload.Error)
	}
	// The message stays a single line; the prose belongs in table mode.
	if strings.Contains(payload.Error.Message, "\n") {
		t.Fatalf("json message should be one line: %q", payload.Error.Message)
	}
}

func TestExplicitlyNamedMissingEnvFileSaysHowToCreateIt(t *testing.T) {
	t.Parallel()
	envFile := filepath.Join(t.TempDir(), "absent.env")
	_, stderr, exitCode := execute(t, config.Environment{"PCENTER_ENV_FILE": envFile}, []string{"app", "info"}, cli.BuildInfo{})
	if exitCode != output.ExitUsage {
		t.Fatalf("exit = %d, want %d", exitCode, output.ExitUsage)
	}
	// Pointing at a file that is not there stays an error rather than silently
	// falling back, but it should now say what to do about it — and carry a
	// code distinct from "credentials missing", because the fix differs.
	//
	// Asserted on the decoded fields rather than the raw text: a Windows path
	// is full of backslashes, and JSON escapes every one of them, so a
	// substring match against the encoded output fails there for a payload
	// that is entirely correct.
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			EnvFile string `json:"envFile"`
			Remedy  string `json:"remedy"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &payload); err != nil {
		t.Fatalf("json error is not parseable: %s", stderr)
	}
	if payload.Error.Code != output.CodeEnvFile {
		t.Fatalf("code = %q, want %q", payload.Error.Code, output.CodeEnvFile)
	}
	if payload.Error.EnvFile != envFile {
		t.Fatalf("envFile = %q, want %q", payload.Error.EnvFile, envFile)
	}
	if !strings.Contains(payload.Error.Remedy, "auth login") {
		t.Fatalf("remedy should name the fixing command: %q", payload.Error.Remedy)
	}

	// Table mode is where a human reads the path back, so check it there too —
	// unescaped, and therefore safe to match literally on every platform.
	_, tableStderr, tableExit := execute(t, config.Environment{"PCENTER_ENV_FILE": envFile}, []string{"--output", "table", "app", "info"}, cli.BuildInfo{})
	if tableExit != output.ExitUsage {
		t.Fatalf("table exit = %d, want %d", tableExit, output.ExitUsage)
	}
	for _, expected := range []string{envFile, "auth login"} {
		if !strings.Contains(tableStderr, expected) {
			t.Fatalf("table error should mention %q: %s", expected, tableStderr)
		}
	}
}

func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return // Unix permission bits are not meaningful here.
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("%s is mode %04o; a file holding a client secret must not be group- or world-readable", path, perm)
	}
}

// The error contract is what automation branches on, so it is pinned here:
// a stable code, a one-line message, structured fields, and an exit code that
// distinguishes cases needing different responses.
func TestErrorContractForAutomation(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	environment := fakeEnvironment(server)

	for _, test := range []struct {
		name     string
		args     []string
		env      config.Environment
		wantCode string
		wantExit int
	}{
		{
			name: "unknown app is an auth-scoped API error", // fakestore 404s an unknown id
			args: []string{"app", "info", "--app-id", "MISSING"},
			env:  environment, wantCode: output.CodeNotFound, wantExit: output.ExitFailure,
		},
		{
			name: "bad credentials are distinguishable from missing ones",
			args: []string{"app", "info"},
			env: config.Environment{
				"MS_STORE_TENANT_ID": "t", "MS_STORE_CLIENT_ID": "c",
				"MS_STORE_CLIENT_SECRET": "s", "MS_STORE_APP_ID": "APP",
				"PCENTER_API_BASE":   rejectingServer(t).APIBase(),
				"PCENTER_LOGIN_BASE": rejectingServer(t).LoginBase(),
			},
			wantCode: output.CodeAuthFailed, wantExit: output.ExitAuth,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, stderr, exitCode := execute(t, test.env, append([]string{"--output", "json"}, test.args...), cli.BuildInfo{})
			var payload struct {
				Error struct {
					Code          string `json:"code"`
					Message       string `json:"message"`
					CorrelationID string `json:"correlationId"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(stderr), &payload); err != nil {
				t.Fatalf("error is not parseable JSON: %s", stderr)
			}
			if payload.Error.Code != test.wantCode {
				t.Fatalf("code = %q, want %q (%s)", payload.Error.Code, test.wantCode, stderr)
			}
			if exitCode != test.wantExit {
				t.Fatalf("exit = %d, want %d", exitCode, test.wantExit)
			}
			if strings.Contains(payload.Error.Message, "\n") {
				t.Fatalf("message must stay one line for machine consumers: %q", payload.Error.Message)
			}
		})
	}
}

func rejectingServer(t *testing.T) *fakestore.Server {
	t.Helper()
	return fakestore.New(t, fakestore.Options{
		AppID: "APP", App: fakestore.App{ID: "APP"}, RejectToken: true,
	})
}

func seedKeychain(t *testing.T, values map[string]string) *config.MemoryKeychain {
	t.Helper()
	keychain := config.NewMemoryKeychain()
	if err := config.SaveKeychain(keychain, values); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}
	return keychain
}

func storedCredentials() map[string]string {
	return map[string]string{
		config.TenantIDName:     "tenant",
		config.ClientIDName:     "client",
		config.ClientSecretName: "secret",
		config.AppIDName:        "APP",
	}
}

func serverEnvironment(server *fakestore.Server) config.Environment {
	return config.Environment{
		"PCENTER_API_BASE":   server.APIBase(),
		"PCENTER_LOGIN_BASE": server.LoginBase(),
	}
}

func TestOperationalCommandReadsCredentialsFromTheKeychain(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	keychain := seedKeychain(t, storedCredentials())
	stdout, stderr, exitCode := executeWithKeychain(t, keychain, serverEnvironment(server),
		[]string{"--output", "json", "app", "info"}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess {
		t.Fatalf("exit = %d stderr = %q stdout = %q", exitCode, stderr, stdout)
	}
}

// recordingKeychain counts calls on top of a MemoryKeychain, so a test can
// prove which branch ran.
type recordingKeychain struct {
	*config.MemoryKeychain
	mu   sync.Mutex
	gets int
	sets int
}

func (r *recordingKeychain) Get(service, account string) (string, error) {
	r.mu.Lock()
	r.gets++
	r.mu.Unlock()
	return r.MemoryKeychain.Get(service, account)
}

func (r *recordingKeychain) Set(service, account, secret string) error {
	r.mu.Lock()
	r.sets++
	r.mu.Unlock()
	return r.MemoryKeychain.Set(service, account, secret)
}

func (r *recordingKeychain) counts() (gets, sets int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gets, r.sets
}

// executeAtHome runs pcenter against a temporary HOME with no
// PCENTER_ENV_FILE, so the default credentials path is the one in use. Tests
// that call it must not be parallel.
func executeAtHome(t *testing.T, keychain config.Keychain, environment config.Environment, args ...string) (stdout, stderr string, exitCode int, defaultFile string) {
	t.Helper()
	defaultFile = useTempHome(t)
	stdout, stderr, exitCode = executeInHome(t, keychain, environment, args...)
	return stdout, stderr, exitCode, defaultFile
}

// useTempHome points HOME at a temporary directory and returns the default
// credentials path under it. Tests that call it must not be parallel.
func useTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	defaultFile, err := config.DefaultEnvFile()
	if err != nil {
		t.Fatal(err)
	}
	return defaultFile
}

// executeInHome runs pcenter with no PCENTER_ENV_FILE against whatever HOME
// the test set up.
func executeInHome(t *testing.T, keychain config.Keychain, environment config.Environment, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var out, errOut bytes.Buffer
	exitCode = cli.Execute(context.Background(), args, cli.Dependencies{
		Stdout:      &out,
		Stderr:      &errOut,
		Environment: environment,
		Now:         func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) },
		Clock:       instantClock{},
		Rand:        cliFixedRand(1),
		Keychain:    keychain,
	})
	return out.String(), errOut.String(), exitCode
}

var accountFlags = []string{"--tenant-id", "tenant", "--client-id", "client", "--client-secret", "secret"}

func loginArgs(extra ...string) []string {
	return append(append([]string{"--output", "json", "auth", "login"}, accountFlags...), extra...)
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist, stat err = %v", path, err)
	}
}

func assertKeychainHolds(t *testing.T, keychain config.Keychain, want map[string]string) {
	t.Helper()
	got, err := config.LoadKeychain(keychain)
	if err != nil {
		t.Fatalf("load keychain: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("keychain = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("keychain[%s] = %q, want %q", key, got[key], value)
		}
	}
}

var accountValues = map[string]string{
	config.TenantIDName: "tenant", config.ClientIDName: "client", config.ClientSecretName: "secret",
}

func TestAuthLoginStoresInTheKeychainWithStorageFlag(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	if err := os.WriteFile(envFile, []byte("# before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := serverEnvironment(server)
	environment["PCENTER_ENV_FILE"] = envFile
	keychain := config.NewMemoryKeychain()
	stdout, stderr, exitCode := executeWithKeychain(t, keychain, environment, loginArgs("--storage", "keychain"), cli.BuildInfo{})
	if exitCode != output.ExitSuccess {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	assertKeychainHolds(t, keychain, accountValues)
	contents, err := os.ReadFile(envFile)
	if err != nil || string(contents) != "# before\n" {
		t.Fatalf("explicit file changed: %q (%v)", contents, err)
	}
	if !strings.Contains(stdout, `"storage":"keychain"`) {
		t.Fatalf("stdout = %s", stdout)
	}
	if strings.Contains(stdout, "secret\"") || strings.Contains(stdout, ":\"secret") {
		t.Fatalf("stdout leaked the secret: %s", stdout)
	}
}

func TestAuthLoginWithEnvFileStillWritesTheFile(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	keychain := config.NewMemoryKeychain()
	stdout, stderr, exitCode := executeWithKeychain(t, keychain, serverEnvironment(server),
		append([]string{"--env-file", envFile}, loginArgs()...), cli.BuildInfo{})
	if exitCode != output.ExitSuccess || !strings.Contains(stdout, `"storage":"file"`) {
		t.Fatalf("exit = %d stdout = %s stderr = %q", exitCode, stdout, stderr)
	}
	assertKeychainHolds(t, keychain, map[string]string{})
	if _, err := os.Stat(envFile); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

func TestAuthLoginDefaultsToTheKeychain(t *testing.T) {
	server := fullServer(t)
	keychain := config.NewMemoryKeychain()
	stdout, stderr, exitCode, defaultFile := executeAtHome(t, keychain, serverEnvironment(server), loginArgs()...)
	if exitCode != output.ExitSuccess || !strings.Contains(stdout, `"storage":"keychain"`) {
		t.Fatalf("exit = %d stdout = %s stderr = %q", exitCode, stdout, stderr)
	}
	assertKeychainHolds(t, keychain, accountValues)
	assertAbsent(t, defaultFile)
}

func TestAuthLoginWithoutAKeychainFails(t *testing.T) {
	server := fullServer(t)
	_, stderr, exitCode, defaultFile := executeAtHome(t, nil, fakeEnvironment(server), "--output", "json", "auth", "login")
	if exitCode != output.ExitFailure {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	assertAbsent(t, defaultFile)
}

func TestAuthLoginFailsWhenTheKeychainCannotBeRead(t *testing.T) {
	server := fullServer(t)
	keychain := config.NewMemoryKeychain()
	keychain.Err = errors.New("keychain locked")
	_, stderr, exitCode, defaultFile := executeAtHome(t, keychain, serverEnvironment(server), loginArgs()...)
	if exitCode != output.ExitFailure {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	for _, expected := range []string{"--storage file", "PCENTER_BYPASS_KEYCHAIN"} {
		if !strings.Contains(stderr, expected) {
			t.Fatalf("stderr should mention %q: %s", expected, stderr)
		}
	}
	assertAbsent(t, defaultFile)
}

func TestAuthLoginFailsWhenTheKeychainWriteFails(t *testing.T) {
	for name, extra := range map[string][]string{"explicit": {"--storage", "keychain"}, "default": nil} {
		t.Run(name, func(t *testing.T) {
			server := fullServer(t)
			inner := config.NewMemoryKeychain()
			inner.SetErr = errors.New("write refused")
			keychain := &recordingKeychain{MemoryKeychain: inner}
			_, stderr, exitCode, defaultFile := executeAtHome(t, keychain, serverEnvironment(server), loginArgs(extra...)...)
			if _, sets := keychain.counts(); sets == 0 {
				t.Fatal("Set was never attempted")
			}
			if exitCode != output.ExitFailure {
				t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
			}
			for _, expected := range []string{"store credentials in the keychain", "--storage file"} {
				if !strings.Contains(stderr, expected) {
					t.Fatalf("stderr should mention %q: %s", expected, stderr)
				}
			}
			assertAbsent(t, defaultFile)
		})
	}
}

func TestAuthLoginStorageFileCopiesKeychainValues(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	keychain := seedKeychain(t, accountValues)
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	environment := serverEnvironment(server)
	environment["PCENTER_ENV_FILE"] = envFile
	_, stderr, exitCode := executeWithKeychain(t, keychain, environment,
		[]string{"--output", "json", "auth", "login", "--storage", "file", "--app-id", "APP"}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	contents, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"MS_STORE_TENANT_ID=tenant", "MS_STORE_CLIENT_ID=client", "MS_STORE_CLIENT_SECRET=secret", "MS_STORE_APP_ID=APP"} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("file missing %q: %s", expected, contents)
		}
	}
}

func TestAuthLoginStorageFileWarnsWhenTheEnvironmentHidesTheKeychain(t *testing.T) {
	for name, seeded := range map[string]bool{"old secret": true, "empty": false} {
		t.Run(name, func(t *testing.T) {
			server := fullServer(t)
			inner := config.NewMemoryKeychain()
			if seeded {
				inner = seedKeychain(t, map[string]string{config.ClientSecretName: "old"})
			}
			keychain := &recordingKeychain{MemoryKeychain: inner}
			stdout, stderr, exitCode, _ := executeAtHome(t, keychain, fakeEnvironment(server),
				"--output", "json", "auth", "login", "--storage", "file")
			if exitCode != output.ExitSuccess {
				t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
			}
			if gets, _ := keychain.counts(); gets == 0 {
				t.Fatal("login never read the keychain")
			}
			warned := strings.Contains(stdout, "takes precedence")
			if warned != seeded {
				t.Fatalf("precedence warning = %v, want %v: %s", warned, seeded, stdout)
			}
		})
	}
}

func TestAuthLoginRejectsMixedEnvironmentAccount(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	cases := []struct {
		name     string
		args     []string
		fileIDs  bool
		wantExit int
	}{
		{"ids from flags", []string{"--tenant-id", "tenant", "--client-id", "client"}, false, output.ExitUsage},
		{"ids from the file", []string{"--storage", "keychain"}, true, output.ExitUsage},
		{"everything from flags", append([]string{"--storage", "keychain"}, accountFlags...), false, output.ExitSuccess},
	}
	for _, c := range cases {
		keychain := seedKeychain(t, map[string]string{config.ClientSecretName: "old"})
		envFile := filepath.Join(t.TempDir(), "credentials.env")
		if c.fileIDs {
			if err := config.Save(envFile, map[string]string{config.TenantIDName: "tenant", config.ClientIDName: "client"}); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(envFile, []byte("# empty\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		environment := serverEnvironment(server)
		environment["PCENTER_ENV_FILE"] = envFile
		environment[config.ClientSecretName] = "env-secret"
		_, stderr, exitCode := executeWithKeychain(t, keychain, environment,
			append([]string{"--output", "json", "auth", "login"}, c.args...), cli.BuildInfo{})
		if exitCode != c.wantExit {
			t.Fatalf("%s: exit = %d, want %d (stderr %q)", c.name, exitCode, c.wantExit, stderr)
		}
		want := map[string]string{config.ClientSecretName: "old"}
		if c.wantExit == output.ExitSuccess {
			want = accountValues
		}
		assertKeychainHolds(t, keychain, want)
	}
}

func TestAuthLoginStorageFileRejectsQuotedValues(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	for _, secret := range []string{`"quoted`, "padded "} {
		keychain := seedKeychain(t, map[string]string{
			config.TenantIDName: "tenant", config.ClientIDName: "client", config.ClientSecretName: secret,
		})
		envFile := filepath.Join(t.TempDir(), "credentials.env")
		environment := serverEnvironment(server)
		environment["PCENTER_ENV_FILE"] = envFile
		_, stderr, exitCode := executeWithKeychain(t, keychain, environment,
			[]string{"--output", "json", "auth", "login", "--storage", "file"}, cli.BuildInfo{})
		if exitCode != output.ExitUsage || !strings.Contains(stderr, config.ClientSecretName) {
			t.Fatalf("secret %q: exit = %d stderr = %q", secret, exitCode, stderr)
		}
		assertAbsent(t, envFile)
	}
}

func TestAuthLoginRejectsMultilineValues(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	envFile := seededEnvFile(t, "# before\n")
	environment := serverEnvironment(server)
	environment["PCENTER_ENV_FILE"] = envFile
	keychain := config.NewMemoryKeychain()
	_, stderr, exitCode := executeWithKeychain(t, keychain, environment, []string{"--output", "json", "auth", "login",
		"--storage", "keychain", "--tenant-id", "tenant", "--client-id", "client", "--client-secret", "two\nlines"}, cli.BuildInfo{})
	if exitCode != output.ExitUsage || !strings.Contains(stderr, "line break") {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	if contents, err := os.ReadFile(envFile); err != nil || string(contents) != "# before\n" {
		t.Fatalf("file changed: %q (%v)", contents, err)
	}
	assertKeychainHolds(t, keychain, map[string]string{})
}

func TestAuthLoginBypassZeroKeepsTheKeychain(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	keychain := config.NewMemoryKeychain()
	environment := serverEnvironment(server)
	environment[config.BypassKeychainEnv] = "0"
	stdout, stderr, exitCode := executeWithKeychain(t, keychain, environment, loginArgs("--storage", "keychain"), cli.BuildInfo{})
	if exitCode != output.ExitSuccess || !strings.Contains(stdout, `"storage":"keychain"`) {
		t.Fatalf("bypass=0: exit = %d stdout = %s stderr = %q", exitCode, stdout, stderr)
	}
}

func TestAuthLoginBypassWritesTheDefaultFile(t *testing.T) {
	server := fullServer(t)
	keychain := &recordingKeychain{MemoryKeychain: config.NewMemoryKeychain()}
	environment := serverEnvironment(server)
	environment[config.BypassKeychainEnv] = "1"
	stdout, stderr, exitCode, defaultFile := executeAtHome(t, keychain, environment, loginArgs()...)
	if exitCode != output.ExitSuccess || !strings.Contains(stdout, `"storage":"file"`) {
		t.Fatalf("exit = %d stdout = %s stderr = %q", exitCode, stdout, stderr)
	}
	if _, err := os.Stat(defaultFile); err != nil {
		t.Fatalf("default file not written: %v", err)
	}
	if gets, sets := keychain.counts(); gets != 0 || sets != 0 {
		t.Fatalf("bypassed keychain was used: %d gets, %d sets", gets, sets)
	}
}

func TestAuthLoginRequiresValuesToRepairAnInvalidItem(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	keychain := config.NewMemoryKeychain()
	if err := keychain.Set(config.KeychainService, config.KeychainAccount, "not json"); err != nil {
		t.Fatal(err)
	}
	environment := fakeEnvironment(server)
	environment["PCENTER_ENV_FILE"] = seededEnvFile(t, "# empty\n")
	_, stderr, exitCode := executeWithKeychain(t, keychain, environment,
		[]string{"--output", "json", "auth", "login", "--storage", "keychain"}, cli.BuildInfo{})
	if exitCode != output.ExitUsage || !strings.Contains(stderr, "--client-secret") {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
}

func TestAuthLoginBypassRejectsStorageKeychain(t *testing.T) {
	t.Parallel()
	environment := config.Environment{config.BypassKeychainEnv: "1"}
	_, stderr, exitCode := executeWithKeychain(t, config.NewMemoryKeychain(), environment, loginArgs("--storage", "keychain"), cli.BuildInfo{})
	if exitCode != output.ExitUsage || !strings.Contains(stderr, config.BypassKeychainEnv) {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
}

func TestAuthLoginStorageKeychainRejectsAMissingExplicitFile(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	environment := serverEnvironment(server)
	environment["PCENTER_ENV_FILE"] = filepath.Join(t.TempDir(), "absent.env")
	keychain := config.NewMemoryKeychain()
	_, stderr, exitCode := executeWithKeychain(t, keychain, environment, loginArgs("--storage", "keychain"), cli.BuildInfo{})
	if exitCode != output.ExitUsage {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	assertKeychainHolds(t, keychain, map[string]string{})
}

func TestAuthLoginRejectsUnknownStorage(t *testing.T) {
	t.Parallel()
	_, _, exitCode := execute(t, config.Environment{}, loginArgs("--storage", "vault"), cli.BuildInfo{})
	if exitCode != output.ExitUsage {
		t.Fatalf("exit = %d, want usage", exitCode)
	}
}

func logoutWith(t *testing.T, keychain config.Keychain, environment config.Environment, envFile string) (string, string, int) {
	t.Helper()
	return executeWithKeychain(t, keychain, environment,
		[]string{"--output", "json", "--env-file", envFile, "auth", "logout", "--yes"}, cli.BuildInfo{})
}

func seededEnvFile(t *testing.T, contents string) string {
	t.Helper()
	envFile := filepath.Join(t.TempDir(), "credentials.env")
	if err := os.WriteFile(envFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return envFile
}

func TestAuthLogoutClearsTheKeychainAndTheFile(t *testing.T) {
	t.Parallel()
	keychain := seedKeychain(t, accountValues)
	envFile := seededEnvFile(t, "MS_STORE_TENANT_ID=tenant\n")
	stdout, stderr, exitCode := logoutWith(t, keychain, config.Environment{}, envFile)
	if exitCode != output.ExitSuccess || !strings.Contains(stdout, `"removed":true`) || !strings.Contains(stdout, `"keychain":"removed"`) {
		t.Fatalf("exit = %d stdout = %s stderr = %q", exitCode, stdout, stderr)
	}
	assertAbsent(t, envFile)
	assertKeychainHolds(t, keychain, map[string]string{})

	stdout, stderr, exitCode = logoutWith(t, keychain, config.Environment{}, envFile)
	if exitCode != output.ExitSuccess || !strings.Contains(stdout, `"keychain":"nothing to remove"`) {
		t.Fatalf("second logout: exit = %d stdout = %s stderr = %q", exitCode, stdout, stderr)
	}
}

func TestAuthLogoutBypassLeavesTheKeychain(t *testing.T) {
	t.Parallel()
	keychain := seedKeychain(t, accountValues)
	envFile := seededEnvFile(t, "MS_STORE_TENANT_ID=tenant\n")
	stdout, stderr, exitCode := logoutWith(t, keychain, config.Environment{config.BypassKeychainEnv: "1"}, envFile)
	if exitCode != output.ExitSuccess || !strings.Contains(stdout, `"keychain":"bypassed"`) {
		t.Fatalf("exit = %d stdout = %s stderr = %q", exitCode, stdout, stderr)
	}
	assertKeychainHolds(t, keychain, accountValues)
}

func TestAuthLogoutKeepsTheKeychainWithoutConfirmation(t *testing.T) {
	t.Parallel()
	keychain := seedKeychain(t, accountValues)
	envFile := seededEnvFile(t, "MS_STORE_TENANT_ID=tenant\n")
	_, _, exitCode := executeWithKeychain(t, keychain, config.Environment{},
		[]string{"--env-file", envFile, "auth", "logout"}, cli.BuildInfo{})
	if exitCode != output.ExitUsage {
		t.Fatalf("exit = %d, want usage", exitCode)
	}
	assertKeychainHolds(t, keychain, accountValues)
}

func TestAuthLogoutDeletesAMalformedKeychainItem(t *testing.T) {
	t.Parallel()
	keychain := config.NewMemoryKeychain()
	if err := keychain.Set(config.KeychainService, config.KeychainAccount, "not json"); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exitCode := logoutWith(t, keychain, config.Environment{}, filepath.Join(t.TempDir(), "absent.env"))
	if exitCode != output.ExitSuccess || !strings.Contains(stdout, `"keychain":"removed"`) {
		t.Fatalf("exit = %d stdout = %s stderr = %q", exitCode, stdout, stderr)
	}
}

func TestAuthLogoutFailsWhenTheKeychainDeleteFails(t *testing.T) {
	t.Parallel()
	keychain := seedKeychain(t, accountValues)
	keychain.DeleteErr = errors.New("delete refused")
	envFile := seededEnvFile(t, "MS_STORE_TENANT_ID=tenant\n")
	_, stderr, exitCode := logoutWith(t, keychain, config.Environment{}, envFile)
	if exitCode != output.ExitFailure || !strings.Contains(stderr, "keychain") {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	assertAbsent(t, envFile)
}

func TestAuthLogoutWorksWithAMalformedEnvFile(t *testing.T) {
	t.Parallel()
	keychain := seedKeychain(t, accountValues)
	envFile := seededEnvFile(t, "not an env line\n")
	stdout, stderr, exitCode := logoutWith(t, keychain, config.Environment{}, envFile)
	if exitCode != output.ExitSuccess || !strings.Contains(stdout, `"keychain":"removed"`) {
		t.Fatalf("exit = %d stdout = %s stderr = %q", exitCode, stdout, stderr)
	}
	assertAbsent(t, envFile)
}

func TestAuthLogoutRefusesADirectory(t *testing.T) {
	t.Parallel()
	keychain := seedKeychain(t, accountValues)
	directory := t.TempDir()
	_, stderr, exitCode := logoutWith(t, keychain, config.Environment{}, directory)
	if exitCode != output.ExitFailure || !strings.Contains(stderr, "is a directory") {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		t.Fatalf("directory should survive: %v", err)
	}
	assertKeychainHolds(t, keychain, map[string]string{})
}

func TestAuthLogoutFailsWhenTheItemSurvivesDelete(t *testing.T) {
	t.Parallel()
	keychain := seedKeychain(t, accountValues)
	keychain.KeepOnDelete = true
	_, stderr, exitCode := logoutWith(t, keychain, config.Environment{}, filepath.Join(t.TempDir(), "absent.env"))
	if exitCode != output.ExitFailure || !strings.Contains(stderr, "still present") {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
}

func TestAuthLogoutFailsWithAnUnreachableKeychain(t *testing.T) {
	t.Parallel()
	keychain := config.NewMemoryKeychain()
	keychain.Err = errors.New("locked")
	envFile := seededEnvFile(t, "MS_STORE_TENANT_ID=tenant\n")
	_, stderr, exitCode := logoutWith(t, keychain, config.Environment{}, envFile)
	if exitCode != output.ExitFailure || !strings.Contains(stderr, config.BypassKeychainEnv) {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	assertAbsent(t, envFile)
}

func TestAuthStatusOfflineReportsKeychainSources(t *testing.T) {
	t.Parallel()
	keychain := seedKeychain(t, storedCredentials())
	stdout, stderr, exitCode := executeWithKeychain(t, keychain, config.Environment{},
		[]string{"--output", "json", "auth", "status", "--offline"}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	var payload struct {
		Sources  map[string]string `json:"sources"`
		Keychain string            `json:"keychain"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode %s: %v", stdout, err)
	}
	for _, name := range config.AllNames {
		if payload.Sources[name] != "keychain" {
			t.Fatalf("%s source = %q, want keychain", name, payload.Sources[name])
		}
	}
	if payload.Keychain != "found" {
		t.Fatalf("keychain = %q, want found", payload.Keychain)
	}
}

func TestAuthStatusReadsTheKeychainOnce(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	keychain := &recordingKeychain{MemoryKeychain: seedKeychain(t, storedCredentials())}
	_, stderr, exitCode := executeWithKeychain(t, keychain, serverEnvironment(server),
		[]string{"--output", "json", "auth", "status"}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	if gets, _ := keychain.counts(); gets != 1 {
		t.Fatalf("keychain read %d times, want 1", gets)
	}

	absent := filepath.Join(t.TempDir(), "absent.env")
	for name, environment := range map[string]config.Environment{
		"complete":   fakeEnvironment(server),
		"incomplete": {config.TenantIDName: "tenant"},
	} {
		_, stderr, exitCode := executeWithKeychain(t, config.NewMemoryKeychain(), environment,
			[]string{"--output", "json", "--env-file", absent, "auth", "status"}, cli.BuildInfo{})
		if exitCode != output.ExitUsage || !strings.Contains(stderr, `"env_file"`) {
			t.Fatalf("%s: exit = %d stderr = %q", name, exitCode, stderr)
		}
	}
}

func TestAuthDoctorReportsTheKeychainCheck(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	keychain := seedKeychain(t, storedCredentials())
	stdout, stderr, exitCode := executeWithKeychain(t, keychain, serverEnvironment(server),
		[]string{"--output", "json", "auth", "doctor"}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess {
		t.Fatalf("exit = %d stdout = %s stderr = %q", exitCode, stdout, stderr)
	}
	report := decodeDoctor(t, stdout)
	if !report.OK || report.status("keychain") != "ok" {
		t.Fatalf("doctor = %s", stdout)
	}
}

func TestAuthDoctorWarnsWhenTheKeychainShadowsTheDefaultFile(t *testing.T) {
	server := fullServer(t)
	keychain := seedKeychain(t, storedCredentials())
	defaultFile := useTempHome(t)
	if err := config.Save(defaultFile, accountValues); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exitCode := executeInHome(t, keychain, serverEnvironment(server), "--output", "json", "auth", "doctor")
	if exitCode != output.ExitSuccess {
		t.Fatalf("exit = %d stdout = %s stderr = %q", exitCode, stdout, stderr)
	}
	report := decodeDoctor(t, stdout)
	if !report.OK || report.status("shadowed file") != "warn" {
		t.Fatalf("doctor = %s", stdout)
	}
}

type doctorReport struct {
	OK     bool `json:"ok"`
	Checks []struct {
		Check  string `json:"check"`
		Status string `json:"status"`
	} `json:"checks"`
}

func (r doctorReport) status(check string) string {
	for _, c := range r.Checks {
		if c.Check == check {
			return c.Status
		}
	}
	return ""
}

func decodeDoctor(t *testing.T, stdout string) doctorReport {
	t.Helper()
	var report doctorReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode %s: %v", stdout, err)
	}
	return report
}

func TestAuthDoctorFailsOnAMissingExplicitFile(t *testing.T) {
	t.Parallel()
	server := fullServer(t)
	keychain := seedKeychain(t, accountValues)
	absent := filepath.Join(t.TempDir(), "absent.env")
	stdout, _, exitCode := executeWithKeychain(t, keychain, serverEnvironment(server),
		[]string{"--output", "json", "--env-file", absent, "auth", "doctor"}, cli.BuildInfo{})
	report := decodeDoctor(t, stdout)
	if exitCode == output.ExitSuccess || report.OK || report.status("env file") != "fail" {
		t.Fatalf("exit = %d doctor = %s", exitCode, stdout)
	}
}

func TestAuthStatusOfflineShowsAMalformedDefaultFile(t *testing.T) {
	defaultFile := useTempHome(t)
	if err := os.MkdirAll(filepath.Dir(defaultFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultFile, []byte("not an env line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, _ := executeInHome(t, config.NewMemoryKeychain(), config.Environment{}, "--output", "json", "auth", "status", "--offline")
	if !strings.Contains(stdout, `"envFileError"`) {
		t.Fatalf("offline status should report the file error: stdout = %s stderr = %q", stdout, stderr)
	}
	tableOut, _, _ := executeInHome(t, config.NewMemoryKeychain(), config.Environment{}, "--output", "table", "auth", "status", "--offline")
	if !strings.Contains(tableOut, "expected KEY=VALUE") {
		t.Fatalf("table status should show the file error: %s", tableOut)
	}
}
