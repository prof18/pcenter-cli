package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/fakestore"
)

var binaryPath string

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "pcenter-e2e-")
	if err != nil {
		panic(err)
	}
	binaryName := "pcenter"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath = filepath.Join(tempDir, binaryName)
	command := exec.CommandContext(context.Background(), "go", "build", "-o", binaryPath, "../../cmd/pcenter")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		panic(err)
	}
	exitCode := m.Run()
	if err := os.RemoveAll(tempDir); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "remove E2E temp directory:", err)
		exitCode = 1
	}
	os.Exit(exitCode)
}

func TestCompiledBinaryReadOnlyCommands(t *testing.T) {
	t.Parallel()
	server := e2eServer(t)
	environment := serverEnvironment(server)

	stdout, stderr, exitCode := run(t, environment, "--output", "json", "app", "info")
	if exitCode != 0 || stderr != "" || !json.Valid([]byte(stdout)) || !strings.Contains(stdout, "Example") {
		t.Fatalf("app info stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
	}

	stdout, stderr, exitCode = run(t, environment, "--output", "table", "rollout", "status")
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "PackageRolloutInProgress") || !strings.Contains(stdout, "fallback") {
		t.Fatalf("rollout stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
	}
}

func TestCompiledBinaryReviewsAllAndUsageExit(t *testing.T) {
	t.Parallel()
	server := e2eServer(t)
	environment := serverEnvironment(server)

	stdout, stderr, exitCode := run(t, environment, "--output", "json", "reviews", "list", "--all")
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "one") || !strings.Contains(stdout, "two") {
		t.Fatalf("reviews stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
	}

	stdout, stderr, exitCode = run(t, environment, "--output", "json", "reviews", "list", "--top", "10001")
	if exitCode != 2 || stdout != "" || !json.Valid([]byte(strings.TrimSpace(stderr))) {
		t.Fatalf("usage stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
	}
}

func TestCompiledBinaryListingPullThenDryRun(t *testing.T) {
	t.Parallel()
	server := e2eServer(t)
	environment := serverEnvironment(server)
	dir := t.TempDir()

	stdout, stderr, exitCode := run(t, environment, "--output", "json", "listing", "pull", "--dir", dir)
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, `"listingCount":1`) {
		t.Fatalf("pull stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
	}
	stdout, stderr, exitCode = run(t, environment, "--output", "json", "listing", "push", "--dir", dir, "--dry-run")
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, `"hasChanges":false`) || !strings.Contains(stdout, `"body"`) {
		t.Fatalf("dry-run stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
	}
	for _, request := range server.Journal() {
		isStoreMutation := strings.HasPrefix(request.Path, "/v1.0/my/") &&
			(request.Method == http.MethodPost || request.Method == http.MethodPut || request.Method == http.MethodDelete)
		if isStoreMutation {
			t.Fatalf("pull/dry-run mutated Store: %+v", request)
		}
	}
}

func run(t *testing.T, environment []string, args ...string) (string, string, int) {
	t.Helper()
	command := exec.CommandContext(context.Background(), binaryPath, args...)
	command.Env = environment
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatal(err)
		}
		exitCode = exitError.ExitCode()
	}
	return stdout.String(), stderr.String(), exitCode
}

func e2eServer(t *testing.T) *fakestore.Server {
	t.Helper()
	return fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{
			ID:                                 "APP",
			PrimaryName:                        "Example",
			LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published"},
		},
		Submissions: map[string]json.RawMessage{
			"published": json.RawMessage(`{
				"id":"published","status":"Published","targetPublishMode":"Immediate",
				"applicationPackages":[{"fileName":"app.msix","fileStatus":"Uploaded","version":"1.0.0.0"}],
				"packageDeliveryOptions":{"isMandatoryUpdate":false,"packageRollout":{"isPackageRollout":true,"packageRolloutPercentage":90}},
				"listings":{"en-us":{"baseListing":{"title":"Title","description":"Description","features":[],"keywords":[],
				"recommendedHardware":[],"minimumHardware":[],"images":[{"fileName":"legacy.png","fileStatus":"Uploaded","id":"image","imageType":"Screenshot"}]}}}
			}`),
		},
		Rollouts: map[string]fakestore.Rollout{
			"published": {IsPackageRollout: true, PackageRolloutPercentage: 90, PackageRolloutStatus: "PackageRolloutInProgress", FallbackSubmissionID: "fallback"},
		},
		ReviewPages: []fakestore.ReviewPage{
			{Value: []json.RawMessage{json.RawMessage(`{"id":"one","rating":5}`)}, TotalCount: 2},
			{Value: []json.RawMessage{json.RawMessage(`{"id":"two","rating":4}`)}, TotalCount: 2},
		},
	})
}

func serverEnvironment(server *fakestore.Server) []string {
	result := append([]string(nil), os.Environ()...)
	return append(result,
		"MS_STORE_TENANT_ID=tenant",
		"MS_STORE_CLIENT_ID=client",
		"MS_STORE_CLIENT_SECRET=secret",
		"MS_STORE_APP_ID=APP",
		"PCENTER_API_BASE="+server.APIBase(),
		"PCENTER_LOGIN_BASE="+server.LoginBase(),
	)
}
