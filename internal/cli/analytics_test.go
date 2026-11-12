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

func TestAnalyticsUsageDropsUngroupedAllDimensions(t *testing.T) {
	t.Parallel()
	server := analyticsServer(t, "usagedaily", []string{
		`{"date":"2026-07-04","applicationId":"APP","subscriptionName":null,"deviceType":"All","packageVersion":"All","market":"All","dailySessionCount":108,"engagementDurationMinutes":475.4,"dailyActiveUsers":47,"dailyActiveDevices":47,"dailyNewUsers":8,"monthlyActiveUsers":396,"monthlyActiveDevices":391,"monthlyNewUsers":250,"dailyNewDevices":0,"monthlyNewDevices":0}`,
	})
	stdout := runAnalytics(t, server, "usage", "daily", "--from", "2026-07-04")
	want := `[{"date":"2026-07-04","dailyActiveUsers":47,"dailyActiveDevices":47,"dailyNewUsers":8,"dailySessionCount":108,` +
		`"engagementDurationMinutes":475.4,"monthlyActiveUsers":396,"monthlyActiveDevices":391,"monthlyNewUsers":250,"dailyNewDevices":0,"monthlyNewDevices":0}]`
	if stdout != want {
		t.Fatalf("stdout = %s\nwant   = %s", stdout, want)
	}
	if query := analyticsRequests(t, server, "usagedaily")[0]; query["aggregationLevel"] != "" || query["groupby"] != "date" {
		t.Fatalf("usage takes no aggregationLevel: %+v", query)
	}
	server = analyticsServer(t, "usagemonthly", []string{
		`{"date":"2026-06-01","market":"All","monthlyActiveUsers":354,"averageDailyActiveUsers":35.06,"engagementDurationMinutes":6813.200000000002}`,
	})
	stdout = runAnalytics(t, server, "usage", "monthly", "--from", "2026-06-01")
	if want := `[{"date":"2026-06-01","monthlyActiveUsers":354,"engagementDurationMinutes":6813.200000000002,"averageDailyActiveUsers":35.06}]`; stdout != want {
		t.Fatalf("monthly stdout = %s, want %s", stdout, want)
	}
}

func TestAnalyticsRatingsReportsStarCounts(t *testing.T) {
	t.Parallel()
	server := analyticsServer(t, "ratings", []string{
		`{"date":"2026-07-05","packageVersion":null,"applicationId":"APP","market":"IT","osVersion":null,"isRevised":"false","oneStar":0,"twoStars":0,"threeStars":0,"fourStars":1,"fiveStars":2}`,
	})
	stdout := runAnalytics(t, server, "ratings", "--from", "2026-07-01", "--group-by", "market,isRevised")
	want := `[{"date":"2026-07-05","market":"IT","isRevised":"false","oneStar":0,"twoStars":0,"threeStars":0,"fourStars":1,"fiveStars":2}]`
	if stdout != want {
		t.Fatalf("stdout = %s\nwant   = %s", stdout, want)
	}
}

func TestAnalyticsFunnelRenamesFieldsAndFixesTheAcquisitionSpelling(t *testing.T) {
	t.Parallel()
	server := analyticsServer(t, "funnel", []string{
		`{"MetricType":"PageView","UserCount":180}`,
		`{"MetricType":"Acqusition","UserCount":96}`,
	})
	stdout := runAnalytics(t, server, "funnel", "--from", "2026-07-01", "--market", "US")
	if want := `[{"metricType":"PageView","userCount":180},{"metricType":"Acquisition","userCount":96}]`; stdout != want {
		t.Fatalf("stdout = %s, want %s", stdout, want)
	}
	query := analyticsRequests(t, server, "funnel")[0]
	for _, absent := range []string{"groupby", "aggregationLevel", "top", "skip", "orderby"} {
		if _, ok := query[absent]; ok {
			t.Fatalf("funnel query has %s: %+v", absent, query)
		}
	}
	if query["filter"] != "market eq 'US'" {
		t.Fatalf("query = %+v", query)
	}
}

func TestAnalyticsConversionsReportsClicksByChannel(t *testing.T) {
	t.Parallel()
	server := analyticsServer(t, "appchannelconversions", []string{
		`{"date":"2026-07-02","applicationId":"APP","customCampaignId":"","channelType":"Store Traffic","market":null,"clickCount":13,"conversionCount":0}`,
	})
	stdout := runAnalytics(t, server, "conversions", "--from", "2026-07-01", "--group-by", "channelType,customCampaignId")
	if want := `[{"date":"2026-07-02","channelType":"Store Traffic","customCampaignId":"","clickCount":13,"conversionCount":0}]`; stdout != want {
		t.Fatalf("stdout = %s, want %s", stdout, want)
	}
}

func TestAnalyticsAddOnsKeepTheAddOnIDAndPassItsFilter(t *testing.T) {
	t.Parallel()
	server := analyticsServer(t, "inappacquisitions", []string{
		`{"applicationId":"APP","inAppProductName":"Pro","addonProductId":"9NADDON","date":"2026-07-02","acquisitionQuantity":1,"purchasePriceUSDAmount":4.147206,"purchaseTaxUSDAmount":0.686004}`,
	})
	stdout := runAnalytics(t, server, "addons", "acquisitions", "--from", "2026-07-01", "--addon-id", "9NADDON")
	if want := `[{"date":"2026-07-02","addonProductId":"9NADDON","acquisitionQuantity":1,"purchasePriceUSDAmount":4.147206,"purchaseTaxUSDAmount":0.686004}]`; stdout != want {
		t.Fatalf("stdout = %s, want %s", stdout, want)
	}
	if query := analyticsRequests(t, server, "inappacquisitions")[0]; query["inAppProductId"] != "9NADDON" || query["applicationId"] != "APP" {
		t.Fatalf("query = %+v", query)
	}

	server = analyticsServer(t, "subscriptions", []string{
		`{"date":"2026-07-01","applicationId":"APP","subscriptionProductName":"pro.monthly","newCount":2,"renewCount":13,"totalActiveCount":152,"grossSalesBeforeTax":0.0,"currencyCode":"EUR"}`,
	})
	stdout = runAnalytics(t, server, "addons", "subscriptions", "--from", "2026-07-01", "--group-by", "subscriptionProductName")
	if want := `[{"date":"2026-07-01","subscriptionProductName":"pro.monthly","newCount":2,"renewCount":13,"totalActiveCount":152,"grossSalesBeforeTax":0,"currencyCode":"EUR"}]`; stdout != want {
		t.Fatalf("subscriptions stdout = %s, want %s", stdout, want)
	}
	if query := analyticsRequests(t, server, "subscriptions")[0]; query["top"] != "" || query["skip"] != "" {
		t.Fatalf("subscriptions must send no top or skip: %+v", query)
	}
}

func TestAnalyticsFailuresListDetailsAndStackTrace(t *testing.T) {
	t.Parallel()
	server := analyticsServer(t, "failurehits", []string{
		`{"date":"2026-07-05","applicationId":"APP","failureName":"","failureHash":"","symbol":null,"eventType":"crash","packageVersion":"1.20.0.0","osVersion":null,"deviceCount":1.0,"eventCount":1.05263157894737}`,
	})
	stdout := runAnalytics(t, server, "failures", "list", "--from", "2026-07-01", "--group-by", "eventType,failureHash,packageVersion")
	// An unbucketed failure keeps its empty hash: "" is data, null is not.
	if want := `[{"date":"2026-07-05","eventType":"crash","failureHash":"","packageVersion":"1.20.0.0","eventCount":1.05263157894737,"deviceCount":1}]`; stdout != want {
		t.Fatalf("list stdout = %s, want %s", stdout, want)
	}
	if query := analyticsRequests(t, server, "failurehits")[0]; query["orderby"] != "" {
		t.Fatalf("failurehits only orders by groupby fields: %+v", query)
	}

	server = analyticsServer(t, "failuredetails", []string{
		`{"date":"2026-07-12 00:00:00","cabExpirationTime":"2026-08-16 01:37:00","cabDownloadable":false,"failureHash":"hash","cabId":"118","osBuild":"10.0.19043","praid":null}`,
	})
	stdout = runAnalytics(t, server, "failures", "details", "--from", "2026-07-01", "--failure-hash", "hash")
	if want := `[{"date":"2026-07-12","cabExpirationTime":"2026-08-16 01:37:00","cabDownloadable":false,"failureHash":"hash","cabId":"118","osBuild":"10.0.19043"}]`; stdout != want {
		t.Fatalf("details stdout = %s, want %s", stdout, want)
	}
	if query := analyticsRequests(t, server, "failuredetails")[0]; query["failureHash"] != "hash" {
		t.Fatalf("query = %+v", query)
	}

	server = analyticsServer(t, "stacktrace", []string{
		`{"level":"0","image":"FeedFlow","function":"HANG_QUIESCE","offset":"0x0","isBlamedFrame":true}`,
	})
	stdout = runAnalytics(t, server, "failures", "stacktrace", "--cab-id", "118")
	if want := `[{"level":"0","image":"FeedFlow","function":"HANG_QUIESCE","offset":"0x0","isBlamedFrame":true}]`; stdout != want {
		t.Fatalf("stacktrace stdout = %s, want %s", stdout, want)
	}
	query := analyticsRequests(t, server, "stacktrace")[0]
	if query["cabId"] != "118" || query["startDate"] != "" || query["top"] != "" {
		t.Fatalf("stacktrace takes only the CAB id: %+v", query)
	}

	_, stderr, exitCode := execute(t, fakeEnvironment(server), []string{"--output", "json", "analytics", "failures", "stacktrace"}, cli.BuildInfo{})
	if exitCode != output.ExitUsage || !strings.Contains(stderr, "--cab-id is required") {
		t.Fatalf("missing cab id: exit = %d stderr = %q", exitCode, stderr)
	}
}

func TestAnalyticsInsightsComposesTheRequiredDataTypeFilter(t *testing.T) {
	t.Parallel()
	server := analyticsServer(t, "insights", []string{
		`{"id":"8cdb","applicationId":"APP","insightDate":"2026-07-07T00:00:00","dataType":"acquisition","insightDetail":[{"DimensionName":"Market","DimensionValue":"GB","Direction":"Negative","PercentChange":3.3}]}`,
	})
	stdout := runAnalytics(t, server, "insights", "--from", "2026-07-01", "--data-type", "acquisition, Health")
	if want := `[{"id":"8cdb","applicationId":"APP","insightDate":"2026-07-07T00:00:00","dataType":"acquisition","insightDetail":[{"DimensionName":"Market","DimensionValue":"GB","Direction":"Negative","PercentChange":3.3}]}]`; stdout != want {
		t.Fatalf("stdout = %s, want %s", stdout, want)
	}
	query := analyticsRequests(t, server, "insights")[0]
	if query["filter"] != "dataType eq 'acquisition' or dataType eq 'health'" || query["top"] != "" {
		t.Fatalf("query = %+v", query)
	}
	_, _, exitCode := execute(t, fakeEnvironment(server), []string{"--output", "json", "analytics", "insights", "--from", "2026-07-01", "--data-type", "revenue"}, cli.BuildInfo{})
	if exitCode != output.ExitUsage {
		t.Fatalf("unknown data type exit = %d", exitCode)
	}
}

func TestAnalyticsSortsHourlyRowsWithinADay(t *testing.T) {
	t.Parallel()
	server := analyticsServer(t, "failurehits", []string{
		`{"date":"2026-07-02 09:00:00","eventCount":1}`,
		`{"date":"2026-07-02 08:00:00","eventCount":2}`,
		`{"date":"2026-07-03 01:00:00","eventCount":3}`,
	})
	stdout := runAnalytics(t, server, "failures", "list", "--from", "2026-07-01", "--to", "2026-07-02", "--aggregation", "hour")
	if want := `[{"date":"2026-07-02T08:00:00","eventCount":2},{"date":"2026-07-02T09:00:00","eventCount":1}]`; stdout != want {
		t.Fatalf("stdout = %s, want %s", stdout, want)
	}
}
