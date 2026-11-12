package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/cli"
	"github.com/prof18/pcenter-cli/internal/config"
	"github.com/prof18/pcenter-cli/internal/fakestore"
	"github.com/prof18/pcenter-cli/internal/output"
)

// acquisitionServer serves two acquisition pages in the live response shape,
// including the purchase fields pcenter drops and a fractional-looking number.
func acquisitionServer(t *testing.T) *fakestore.Server {
	t.Helper()
	return fakestore.New(t, fakestore.Options{
		AppID: "APP",
		App:   fakestore.App{ID: "APP"},
		AcquisitionPages: []fakestore.ReviewPage{
			{Value: []json.RawMessage{
				json.RawMessage(`{"applicationId":"APP","date":"2026-07-01","osVersion":"Windows 11","market":"US","acquisitionQuantity":3.0,"purchasePriceUSDAmount":0.0}`),
				json.RawMessage(`{"applicationId":"APP","date":"2026-07-01","osVersion":"Windows 10","market":"IT","acquisitionQuantity":2}`),
			}, TotalCount: 3},
			{Value: []json.RawMessage{
				json.RawMessage(`{"applicationId":"APP","date":"2026-07-02","osVersion":"Windows 11","market":"US","acquisitionQuantity":5}`),
			}, TotalCount: 3},
		},
	})
}

func TestAnalyticsAcquisitionsFollowsPagesAndNormalizesRows(t *testing.T) {
	t.Parallel()
	server := acquisitionServer(t)
	stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{
		"--output", "json", "analytics", "acquisitions", "--from", "2026-07-01", "--to", "2026-07-02",
		"--group-by", "osversion, market,date", "--market", "US",
	}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess || stderr != "" {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	want := `[{"date":"2026-07-01","acquisitionQuantity":3,"osVersion":"Windows 11","market":"US"},` +
		`{"date":"2026-07-01","acquisitionQuantity":2,"osVersion":"Windows 10","market":"IT"},` +
		`{"date":"2026-07-02","acquisitionQuantity":5,"osVersion":"Windows 11","market":"US"}]`
	if strings.TrimSpace(stdout) != want {
		t.Fatalf("stdout = %s\nwant   = %s", stdout, want)
	}

	var requests []fakestore.Request
	for _, request := range server.Journal() {
		if request.Path == "/v1.0/my/analytics/appacquisitions" {
			requests = append(requests, request)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("acquisition requests = %d, want 2", len(requests))
	}
	query, err := urlParseQuery(requests[0].Query)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{
		"applicationId": "APP", "startDate": "7/1/2026", "endDate": "7/2/2026", "aggregationLevel": "day",
		"groupby": "date,osVersion,market", "filter": "market eq 'US'", "orderby": "date", "top": "10000", "skip": "0",
	}
	for key, value := range expected {
		if query[key] != value {
			t.Fatalf("query[%s] = %q, want %q (query %+v)", key, query[key], value, query)
		}
	}
}

func TestAnalyticsAcquisitionsDefaultsToPerDayTotalsEndingToday(t *testing.T) {
	t.Parallel()
	server := acquisitionServer(t)
	stdout, stderr, exitCode := execute(t, fakeEnvironment(server), []string{
		"--output", "table", "analytics", "acquisitions", "--from", "2026-07-01",
	}, cli.BuildInfo{})
	if exitCode != output.ExitSuccess || stderr != "" {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 4 || strings.Fields(lines[0])[0] != "DATE" || strings.Fields(lines[0])[1] != "ACQUISITIONS" {
		t.Fatalf("table = %q", stdout)
	}
	var first string
	for _, request := range server.Journal() {
		if request.Path == "/v1.0/my/analytics/appacquisitions" {
			first = request.Query
			break
		}
	}
	query, err := urlParseQuery(first)
	if err != nil {
		t.Fatal(err)
	}
	// The test clock is 2026-07-13.
	if query["endDate"] != "7/13/2026" || query["groupby"] != "date" {
		t.Fatalf("query = %+v", query)
	}
}

func TestAnalyticsAcquisitionsRejectsBadInputBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{},
		{"--from", "07/01/2026"},
		{"--from", "2026-07-01", "--to", "2026-13-01"},
		{"--from", "2026-07-02", "--to", "2026-07-01"},
		{"--from", "2026-07-01", "--aggregation", "year"},
		{"--from", "2026-07-01", "--group-by", "date,price"},
	} {
		_, stderr, exitCode := execute(t, config.Environment{}, append([]string{"--output", "json", "analytics", "acquisitions"}, args...), cli.BuildInfo{})
		if exitCode != output.ExitUsage || !json.Valid([]byte(strings.TrimSpace(stderr))) {
			t.Fatalf("%v exit = %d stderr = %q", args, exitCode, stderr)
		}
	}
}
