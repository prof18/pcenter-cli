package submission_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/prof18/pcenter-cli/internal/submission"
)

func TestBuildPublishBodyMatchesGoldenAndPreservesUnknownNestedFields(t *testing.T) {
	t.Parallel()
	input, err := os.ReadFile("testdata/publish_input.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/publish_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := submission.BuildPublishBody(input, "FeedFlow.msix", 90)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, got, want)
}

func TestBuildPublishBodySortsFourPartVersionsAndRejectsMalformedVersions(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{
      "listings":{"en-us":{}},
      "applicationPackages":[
        {"fileName":"a","version":"1.9.0.0","fileStatus":"Uploaded"},
        {"fileName":"b","version":"1.10.0.0","fileStatus":"Uploaded"},
        {"fileName":"c","version":"1.2.20.0","fileStatus":"Uploaded"}
      ]
    }`)
	got, err := submission.BuildPublishBody(input, "new.msix", 50)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Packages []struct {
			FileName   string `json:"fileName"`
			FileStatus string `json:"fileStatus"`
		} `json:"applicationPackages"`
	}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"b", "a", "c", "new.msix"}
	for index, want := range wantNames {
		if body.Packages[index].FileName != want {
			t.Fatalf("packages = %+v", body.Packages)
		}
		if index > 0 && index < 3 && body.Packages[index].FileStatus != "PendingDelete" {
			t.Fatalf("older package not pending delete: %+v", body.Packages[index])
		}
	}

	bad := json.RawMessage(`{"applicationPackages":[{"version":"1.2","fileName":"bad"}]}`)
	if _, err := submission.BuildPublishBody(bad, "new.msix", 90); err == nil {
		t.Fatal("malformed package version accepted")
	}
}

func TestBuildPublishBodyValidatesArgumentsAndCreatesDeliveryDefaults(t *testing.T) {
	t.Parallel()
	for _, percentage := range []float64{0, -1, 101} {
		if _, err := submission.BuildPublishBody(json.RawMessage(`{}`), "new.msix", percentage); err == nil {
			t.Fatalf("percentage %v accepted", percentage)
		}
	}
	if _, err := submission.BuildPublishBody(json.RawMessage(`{}`), "", 90); err == nil {
		t.Fatal("empty package name accepted")
	}
	got, err := submission.BuildPublishBody(json.RawMessage(`{}`), "new.msix", 90)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(got) || !containsJSONText(got, `"isMandatoryUpdate":false`) || !containsJSONText(got, `"1601-01-01T00:00:00.0000000Z"`) {
		t.Fatalf("defaults missing: %s", got)
	}
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	gotCanonical, _ := json.Marshal(gotValue)
	wantCanonical, _ := json.Marshal(wantValue)
	if string(gotCanonical) != string(wantCanonical) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", gotCanonical, wantCanonical)
	}
}

func containsJSONText(data []byte, text string) bool {
	for index := 0; index+len(text) <= len(data); index++ {
		if string(data[index:index+len(text)]) == text {
			return true
		}
	}
	return false
}
