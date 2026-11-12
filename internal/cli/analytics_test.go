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

// analyticsServer serves one analytics endpoint from raw row pages, in the
// live response shape.
func analyticsServer(t *testing.T, endpoint string, pages ...[]string) *fakestore.Server {
	t.Helper()
	fakePages := make([]fakestore.ReviewPage, 0, len(pages))
	for _, page := range pages {
		value := make([]json.RawMessage, 0, len(page))
		for _, row := range page {
			value = append(value, json.RawMessage(row))
		}
		fakePages = append(fakePages, fakestore.ReviewPage{Value: value})
	}
	return fakestore.New(t, fakestore.Options{
		AppID:          "APP",
		App:            fakestore.App{ID: "APP"},
		AnalyticsPages: map[string][]fakestore.ReviewPage{endpoint: fakePages},
	})
}

// analyticsRequests returns the parsed queries sent to one endpoint.
func analyticsRequests(t *testing.T, server *fakestore.Server, endpoint string) []map[string]string {
	t.Helper()
	var queries []map[string]string
	for _, request := range server.Journal() {
		if request.Path != "/v1.0/my/analytics/"+endpoint {
			continue
		}
		query, err := urlParseQuery(request.Query)
		if err != nil {
			t.Fatal(err)
		}
		queries = append(queries, query)
	}
	return queries
}

// runAnalytics runs a command against the server and returns its stdout.
func runAnalytics(t *testing.T, server *fakestore.Server, args ...string) string {
	t.Helper()
	stdout, stderr, exitCode := execute(t, fakeEnvironment(server), append([]string{"--output", "json", "analytics"}, args...), cli.BuildInfo{})
	if exitCode != output.ExitSuccess || stderr != "" {
		t.Fatalf("%v exit = %d stderr = %q", args, exitCode, stderr)
	}
	return strings.TrimSpace(stdout)
}

func acquisitionServer(t *testing.T) *fakestore.Server {
	t.Helper()
	return analyticsServer(t, "appacquisitions",
		[]string{
			`{"applicationId":"APP","date":"2026-07-01","osVersion":"Windows 11","market":"US","deviceType":null,"acquisitionQuantity":3.0,"purchasePriceUSDAmount":0.0}`,
			`{"applicationId":"APP","date":"2026-07-01","osVersion":"Windows 10","market":"IT","acquisitionQuantity":2}`,
		},
		[]string{`{"applicationId":"APP","date":"2026-07-02","osVersion":"Windows 11","market":"US","acquisitionQuantity":5}`},
	)
}

func TestAnalyticsAcquisitionsFollowsPagesAndNormalizesRows(t *testing.T) {
	t.Parallel()
	server := acquisitionServer(t)
	stdout := runAnalytics(t, server, "acquisitions", "--from", "2026-07-01", "--to", "2026-07-02",
		"--group-by", "osversion, market,date", "--market", "US")
	// Fields come in a fixed order (date, dimensions, metrics); nulls and
	// ungrouped fields are dropped; 3.0 is reported as 3.
	want := `[{"date":"2026-07-01","osVersion":"Windows 11","market":"US","acquisitionQuantity":3,"purchasePriceUSDAmount":0},` +
		`{"date":"2026-07-01","osVersion":"Windows 10","market":"IT","acquisitionQuantity":2},` +
		`{"date":"2026-07-02","osVersion":"Windows 11","market":"US","acquisitionQuantity":5}]`
	if stdout != want {
		t.Fatalf("stdout = %s\nwant   = %s", stdout, want)
	}
	requests := analyticsRequests(t, server, "appacquisitions")
	if len(requests) != 2 {
		t.Fatalf("acquisition requests = %d, want 2", len(requests))
	}
	expected := map[string]string{
		"applicationId": "APP", "startDate": "7/1/2026", "endDate": "7/2/2026", "aggregationLevel": "day",
		"groupby": "date,osVersion,market", "filter": "market eq 'US'", "orderby": "date", "top": "10000", "skip": "0",
	}
	for key, value := range expected {
		if requests[0][key] != value {
			t.Fatalf("query[%s] = %q, want %q (query %+v)", key, requests[0][key], value, requests[0])
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
	if len(lines) != 4 || strings.Fields(lines[0])[0] != "DATE" || strings.Fields(lines[0])[1] != "ACQUISITIONQUANTITY" {
		t.Fatalf("table = %q", stdout)
	}
	query := analyticsRequests(t, server, "appacquisitions")[0]
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

func TestAnalyticsInstallsReportsInstallCounts(t *testing.T) {
	t.Parallel()
	server := analyticsServer(t, "installs", []string{
		`{"date":"2026-07-04","applicationId":"APP","applicationName":"FeedFlow","packageVersion":"1.20.0.0","deviceType":null,"market":null,"osVersion":null,"ageGroup":null,"successfulInstallCount":36}`,
	})
	stdout := runAnalytics(t, server, "installs", "--from", "2026-07-04", "--group-by", "packageVersion", "--aggregation", "week")
	if want := `[{"date":"2026-07-04","packageVersion":"1.20.0.0","successfulInstallCount":36}]`; stdout != want {
		t.Fatalf("stdout = %s, want %s", stdout, want)
	}
	query := analyticsRequests(t, server, "installs")[0]
	if query["groupby"] != "date,packageVersion" || query["aggregationLevel"] != "week" {
		t.Fatalf("query = %+v", query)
	}
}

// The live installs and usage endpoints return newest first whatever orderby
// says, and include a row for the day after endDate. pcenter sorts and keeps
// --to inclusive itself.
func TestAnalyticsSortsByDateAndDropsRowsAfterTo(t *testing.T) {
	t.Parallel()
	server := analyticsServer(t, "installs", []string{
		`{"date":"2026-07-03","successfulInstallCount":3}`,
		`{"date":"2026-07-02","packageVersion":"2","successfulInstallCount":2}`,
		`{"date":"2026-07-02","packageVersion":"1","successfulInstallCount":1}`,
		`{"date":"2026-07-01","successfulInstallCount":9}`,
	})
	stdout := runAnalytics(t, server, "installs", "--from", "2026-07-01", "--to", "2026-07-02", "--group-by", "packageVersion")
	want := `[{"date":"2026-07-01","successfulInstallCount":9},` +
		`{"date":"2026-07-02","packageVersion":"2","successfulInstallCount":2},` +
		`{"date":"2026-07-02","packageVersion":"1","successfulInstallCount":1}]`
	if stdout != want {
		t.Fatalf("stdout = %s\nwant   = %s", stdout, want)
	}
}
