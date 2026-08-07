package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	for _, expected := range []string{envFile, "auth login"} {
		if !strings.Contains(stderr, expected) {
			t.Fatalf("error should mention %q: %s", expected, stderr)
		}
	}
	_, jsonStderr, _ := execute(t, config.Environment{"PCENTER_ENV_FILE": envFile}, []string{"--output", "json", "app", "info"}, cli.BuildInfo{})
	if !strings.Contains(jsonStderr, `"code":"`+output.CodeEnvFile+`"`) {
		t.Fatalf("expected %s code: %s", output.CodeEnvFile, jsonStderr)
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
