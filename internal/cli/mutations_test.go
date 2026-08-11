package cli_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/cli"
	"github.com/prof18/pcenter-cli/internal/fakestore"
	"github.com/prof18/pcenter-cli/internal/output"
)

func TestRolloutMutationCommands(t *testing.T) {
	t.Parallel()
	t.Run("finalize 504 succeeded regression", func(t *testing.T) {
		t.Parallel()
		server := mutationRolloutServer(t, fakestore.MutationScenario{Failures: 2, Status: 504, ApplyOnFailure: true})
		stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{"--output", "json", "rollout", "finalize"}, cli.BuildInfo{})
		if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "PackageRolloutCompleted") {
			t.Fatalf("stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
		}
		if countRequests(server.Journal(), http.MethodPost, "/finalizepackagerollout") != 1 {
			t.Fatalf("finalize was blindly retried: %+v", server.Journal())
		}
	})

	t.Run("set percentage uses query", func(t *testing.T) {
		t.Parallel()
		server := mutationRolloutServer(t, fakestore.MutationScenario{})
		stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{"--output", "json", "rollout", "set-percentage", "55.5"}, cli.BuildInfo{})
		if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "55.5") {
			t.Fatalf("stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
		}
		journal := server.Journal()
		if journal[len(journal)-2].Query != "percentage=55.5" {
			t.Fatalf("set percentage query = %q", journal[len(journal)-2].Query)
		}
	})

	t.Run("halt requires yes and explains clone semantics", func(t *testing.T) {
		t.Parallel()
		server := mutationRolloutServer(t, fakestore.MutationScenario{})
		_, _, exitCode := execute(t, fakeEnvironment(server), []string{"--output", "json", "rollout", "halt"}, cli.BuildInfo{})
		if exitCode != output.ExitUsage {
			t.Fatalf("halt without yes exit = %d", exitCode)
		}
		stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{"--output", "json", "rollout", "halt", "--yes"}, cli.BuildInfo{})
		if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "PackageRolloutStopped") || !strings.Contains(stdout, "halted submission") {
			t.Fatalf("stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
		}
	})
}

func TestSubmissionMutationCommands(t *testing.T) {
	t.Parallel()
	t.Run("delete draft guard and verify", func(t *testing.T) {
		t.Parallel()
		server := mutationDraftServer(t, []string{"PendingCommit"}, fakestore.MutationScenario{})
		_, _, exitCode := execute(t, fakeEnvironment(server), []string{"--output", "json", "submission", "delete-draft"}, cli.BuildInfo{})
		if exitCode != output.ExitUsage {
			t.Fatalf("delete without yes exit = %d", exitCode)
		}
		stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{"--output", "json", "submission", "delete-draft", "--yes"}, cli.BuildInfo{})
		if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "draft") {
			t.Fatalf("stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
		}
	})

	t.Run("commit defaults to pending", func(t *testing.T) {
		t.Parallel()
		server := mutationDraftServer(t, []string{"PreProcessing"}, fakestore.MutationScenario{})
		stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{
			"--output", "json", "submission", "commit", "--poll-seconds", "0", "--poll-attempts", "2",
		}, cli.BuildInfo{})
		if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "PreProcessing") {
			t.Fatalf("stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
		}
	})

	t.Run("watch reaches published", func(t *testing.T) {
		t.Parallel()
		server := mutationDraftServer(t, nil, fakestore.MutationScenario{})
		server.SetStatusQueue("draft", []string{"Certification", "Published"})
		stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{
			"--output", "json", "submission", "watch", "--poll-seconds", "0", "--poll-attempts", "2",
		}, cli.BuildInfo{})
		if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "Published") || !strings.Contains(stdout, "success") {
			t.Fatalf("stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
		}
	})
}

func mutationRolloutServer(t *testing.T, scenario fakestore.MutationScenario) *fakestore.Server {
	t.Helper()
	return fakestore.New(t, fakestore.Options{
		AppID:            "APP",
		App:              fakestore.App{ID: "APP", LastPublishedApplicationSubmission: &fakestore.SubmissionRef{ID: "published"}},
		Submissions:      map[string]json.RawMessage{"published": json.RawMessage(`{"id":"published","status":"Published"}`)},
		Rollouts:         map[string]fakestore.Rollout{"published": {IsPackageRollout: true, PackageRolloutPercentage: 90, PackageRolloutStatus: "PackageRolloutInProgress"}},
		FinalizeScenario: scenario,
	})
}

func mutationDraftServer(t *testing.T, statuses []string, deleteScenario fakestore.MutationScenario) *fakestore.Server {
	t.Helper()
	return fakestore.New(t, fakestore.Options{
		AppID:          "APP",
		App:            fakestore.App{ID: "APP", PendingApplicationSubmission: &fakestore.SubmissionRef{ID: "draft"}},
		Submissions:    map[string]json.RawMessage{"draft": json.RawMessage(`{"id":"draft","status":"PendingCommit"}`)},
		CommitStatuses: statuses, DeleteScenario: deleteScenario,
	})
}

func countRequests(journal []fakestore.Request, method, pathSuffix string) int {
	count := 0
	for _, request := range journal {
		if request.Method == method && strings.HasSuffix(request.Path, pathSuffix) {
			count++
		}
	}
	return count
}

// Running out of poll attempts means pcenter stopped watching, not that the
// submission failed — certification legitimately takes hours. Exiting non-zero
// here turns a healthy release into a red CI job, and makes the documented
// "in-progress" classification unreachable.
func TestSubmissionWatchReportsStillInProgressWithoutFailing(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{ID: "APP",
			PendingApplicationSubmission: &fakestore.SubmissionRef{ID: "pending"},
		},
		Submissions: map[string]json.RawMessage{"pending": json.RawMessage(`{"id":"pending","status":"CommitStarted"}`)},
		StatusQueues: map[string][]string{
			"pending": {"CommitStarted", "CommitStarted", "CommitStarted"},
		},
	})

	stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{
		"--output", "json", "submission", "watch", "--poll-seconds", "0", "--poll-attempts", "2",
	}, cli.BuildInfo{})
	if exitCode != 0 {
		t.Fatalf("exit = %d, want 0; stdout = %q stderr = %q", exitCode, stdout, stderr)
	}
	var result struct {
		Status         string `json:"status"`
		Classification string `json:"classification"`
		Warning        string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("output is not parseable: %s", stdout)
	}
	if result.Status != "CommitStarted" || result.Classification != "in-progress" {
		t.Fatalf("result = %+v, want the last observed status classified in-progress", result)
	}
	if !strings.Contains(result.Warning, "still CommitStarted") {
		t.Fatalf("warning = %q, want it to say the submission is still processing", result.Warning)
	}
}

// A genuinely failed submission must still exit non-zero: the fix above must not
// swallow the case automation actually needs to catch.
func TestSubmissionWatchStillFailsOnAFailedStatus(t *testing.T) {
	t.Parallel()
	server := fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App: fakestore.App{ID: "APP",
			PendingApplicationSubmission: &fakestore.SubmissionRef{ID: "pending"},
		},
		Submissions: map[string]json.RawMessage{"pending": json.RawMessage(`{"id":"pending","status":"CertificationFailed"}`)},
		StatusQueues: map[string][]string{
			"pending": {"CertificationFailed"},
		},
	})

	_, _, exitCode := execute(t, fakeEnvironment(server), []string{
		"submission", "watch", "--poll-seconds", "0", "--poll-attempts", "2",
	}, cli.BuildInfo{})
	if exitCode == 0 {
		t.Fatal("a failed submission must not exit 0")
	}
}
