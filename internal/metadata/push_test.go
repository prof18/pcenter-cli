package metadata_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prof18/pcenter-cli/internal/metadata"
)

func TestBuildPushPlanReportsNoChangesAndPreservesUnmodeledFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	submission := pushSubmissionJSON()
	snapshot, _, err := metadata.SnapshotFromSubmission(dir, "APP", "test", testTime(), submission)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := metadata.BuildPushPlan(dir, snapshot, submission, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasChanges() || len(plan.Uploads) != 0 {
		t.Fatalf("unexpected changes: %+v", plan)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(plan.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, exists := body["serverOnlyTopLevel"]; exists {
		t.Fatal("non-allowlisted top-level field leaked into PUT body")
	}
	assertJSONEqual(t, body["applicationPackages"], json.RawMessage(`[{"fileName":"app.msix","fileStatus":"Uploaded","futurePackage":true}]`))
	assertJSONEqual(t, body["packageDeliveryOptions"], json.RawMessage(`{"isMandatoryUpdate":false,"packageRollout":{"isPackageRollout":false},"futureDelivery":7}`))

	var listings map[string]json.RawMessage
	if err := json.Unmarshal(body["listings"], &listings); err != nil {
		t.Fatal(err)
	}
	var listing map[string]json.RawMessage
	if err := json.Unmarshal(listings["EN-US"], &listing); err != nil {
		t.Fatal(err)
	}
	if string(listing["futureListing"]) != `"kept"` {
		t.Fatalf("future listing field = %s", listing["futureListing"])
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal(listing["baseListing"], &base); err != nil {
		t.Fatal(err)
	}
	if string(base["platformOverrides"]) != `{"desktop":{"future":true}}` {
		t.Fatalf("platformOverrides = %s", base["platformOverrides"])
	}
	if !strings.Contains(string(base["images"]), `"futureImage":"kept"`) {
		t.Fatalf("image unknown field not preserved: %s", base["images"])
	}
}

func TestBuildPushPlanAppliesTextLocaleAndImageChanges(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	submission := pushSubmissionJSON()
	snapshot, _, err := metadata.SnapshotFromSubmission(dir, "APP", "test", testTime(), submission)
	if err != nil {
		t.Fatal(err)
	}
	listing := snapshot.Listings["en-us"]
	listing.Title = "Changed"
	snapshot.Listings["en-us"] = listing
	snapshot.Listings["it"] = metadata.Listing{Title: "Italiano", Features: []string{}, Keywords: []string{}}
	snapshot.Images.Images["it"] = []metadata.ImageEntry{}

	plan, err := metadata.BuildPushPlan(dir, snapshot, submission, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []metadata.ListingChange{
		{Locale: "en-us", Action: "update", Field: "title"},
		{Locale: "it", Action: "add"},
	}
	if !reflect.DeepEqual(plan.ListingChanges, want) {
		t.Fatalf("listing changes = %+v, want %+v", plan.ListingChanges, want)
	}
	if !plan.HasChanges() {
		t.Fatal("plan should contain changes")
	}
	var body struct {
		Listings map[string]json.RawMessage `json:"listings"`
	}
	if err := json.Unmarshal(plan.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Listings) != 2 || body.Listings["EN-US"] == nil || body.Listings["it"] == nil {
		t.Fatalf("body listings = %+v", body.Listings)
	}
	if !strings.Contains(string(body.Listings["EN-US"]), `"title":"Changed"`) {
		t.Fatalf("updated listing = %s", body.Listings["EN-US"])
	}
}

func TestBuildPushPlanGuardsLocaleRemoval(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	submission := pushSubmissionJSON()
	snapshot, _, err := metadata.SnapshotFromSubmission(dir, "APP", "test", testTime(), submission)
	if err != nil {
		t.Fatal(err)
	}
	delete(snapshot.Listings, "en-us")
	snapshot.Listings["it"] = metadata.Listing{Title: "Italiano", Features: []string{}, Keywords: []string{}}
	snapshot.Images.Images["it"] = []metadata.ImageEntry{}

	_, err = metadata.BuildPushPlan(dir, snapshot, submission, false)
	if err == nil || !strings.Contains(err.Error(), "allow-locale-removal") {
		t.Fatalf("guard error = %v", err)
	}
	plan, err := metadata.BuildPushPlan(dir, snapshot, submission, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []metadata.ListingChange{{Locale: "en-us", Action: "remove"}, {Locale: "it", Action: "add"}}
	if !reflect.DeepEqual(plan.ListingChanges, want) {
		t.Fatalf("listing changes = %+v, want %+v", plan.ListingChanges, want)
	}
}

func pushSubmissionJSON() json.RawMessage {
	return json.RawMessage(`{
		"id":"submission",
		"targetPublishMode":"SpecificDate",
		"targetPublishDate":"2026-08-01T00:00:00Z",
		"visibility":"Public",
		"serverOnlyTopLevel":"drop",
		"applicationPackages":[{"fileName":"app.msix","fileStatus":"Uploaded","futurePackage":true}],
		"packageDeliveryOptions":{"isMandatoryUpdate":false,"packageRollout":{"isPackageRollout":false},"futureDelivery":7},
		"listings":{"EN-US":{"futureListing":"kept","baseListing":{
			"title":"Title","description":"Description","features":["One"],"keywords":["rss"],
			"copyrightAndTrademarkInfo":"Copyright","licenseTerms":"Terms",
			"recommendedHardware":["Keyboard"],"minimumHardware":"Any PC","releaseNotes":"Keep notes",
			"platformOverrides":{"desktop":{"future":true}},
			"images":[{"fileName":"legacy.png","fileStatus":"Uploaded","id":"image","description":"Screenshot","imageType":"Screenshot","futureImage":"kept"}]
		}}}
	}`)
}

func assertJSONEqual(t *testing.T, actual, expected json.RawMessage) {
	t.Helper()
	var actualValue, expectedValue any
	if err := json.Unmarshal(actual, &actualValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expected, &expectedValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualValue, expectedValue) {
		t.Fatalf("JSON = %s, want %s", actual, expected)
	}
}

func testTime() time.Time {
	return time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
}
