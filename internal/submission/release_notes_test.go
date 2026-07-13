package submission_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/submission"
)

func TestApplyReleaseNotesStringArrayCaseInsensitiveAndWarnings(t *testing.T) {
	t.Parallel()
	submissionJSON := json.RawMessage(`{
      "id":"draft",
      "listings":{
        "en-US":{"baseListing":{"description":"English"},"unknown":"keep"},
        "it":{"baseListing":{"description":"Italiano","releaseNotes":"old"}}
      }
    }`)
	notesJSON := []byte(`{"notes":{"EN-us":"  One line  ","IT":[" First ","",null," Second "],"fr":"unused"}}`)
	updated, warnings, err := submission.ApplyReleaseNotes(submissionJSON, notesJSON, "notes.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "fr") {
		t.Fatalf("warnings = %v", warnings)
	}
	if !containsJSONText(updated, `"releaseNotes":"One line"`) || !containsJSONText(updated, `"releaseNotes":"First\r\nSecond"`) || !containsJSONText(updated, `"unknown":"keep"`) {
		t.Fatalf("updated submission = %s", updated)
	}
}

func TestApplyReleaseNotesRejectsMissingInvalidAndEmptyLocales(t *testing.T) {
	t.Parallel()
	submissionJSON := json.RawMessage(`{"listings":{"en-us":{"baseListing":{}},"it":{"baseListing":{}}}}`)
	for _, test := range []struct {
		name  string
		notes string
		want  string
	}{
		{name: "missing", notes: `{"notes":{"en-us":"hello"}}`, want: "it"},
		{name: "wrong top level", notes: `{"releaseNotes":{"en-us":"hello","it":"ciao"}}`, want: "notes"},
		{name: "invalid value", notes: `{"notes":{"en-us":3,"it":"ciao"}}`, want: "string or an array"},
		{name: "non-string array", notes: `{"notes":{"en-us":["ok",3],"it":"ciao"}}`, want: "array of strings"},
		{name: "empty", notes: `{"notes":{"en-us":"  ","it":"ciao"}}`, want: "empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := submission.ApplyReleaseNotes(submissionJSON, []byte(test.notes), "notes.json")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestApplyReleaseNotesRequiresListingsAndBaseListing(t *testing.T) {
	t.Parallel()
	if _, _, err := submission.ApplyReleaseNotes(json.RawMessage(`{}`), []byte(`{"notes":{"en-us":"x"}}`), "notes.json"); err == nil {
		t.Fatal("submission without listings accepted")
	}
	if _, _, err := submission.ApplyReleaseNotes(json.RawMessage(`{"listings":{"en-us":{}}}`), []byte(`{"notes":{"en-us":"x"}}`), "notes.json"); err == nil {
		t.Fatal("listing without baseListing accepted")
	}
}
