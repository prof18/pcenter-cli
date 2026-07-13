package metadata_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prof18/pcenter-cli/internal/metadata"
)

func TestSnapshotFromSubmissionExtractsEditableFieldsAndMatchesLocalImages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "images", "en-us", "screen.png"), 1366, 768)
	raw := json.RawMessage(`{
      "id":"submission",
      "listings":{
        "EN-US":{
          "baseListing":{
            "title":"Title","description":"Description","features":["One"],"keywords":["rss"],
            "copyrightAndTrademarkInfo":"Copyright","licenseTerms":"Terms",
            "recommendedHardware":["Keyboard"],"minimumHardware":"Any PC",
            "releaseNotes":"not written","privacyPolicy":"obsolete",
            "images":[
              {"fileName":"en-us/screen.png","fileStatus":"Uploaded","id":"local","description":"Local","imageType":"Screenshot"},
              {"fileName":"legacy-name.png","fileStatus":"Uploaded","id":"remote","description":"Remote","imageType":"Screenshot"}
            ]
          },
          "platformOverrides":{"Windows81":{"description":"preserved on push, not written"}}
        }
      }
    }`)
	pulledAt := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	snapshot, serverImages, err := metadata.SnapshotFromSubmission(dir, "APP", "pcenter test", pulledAt, raw)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Marker.AppID != "APP" || snapshot.Marker.SourceSubmissionID != "submission" || snapshot.Marker.PulledAt != pulledAt {
		t.Fatalf("marker = %+v", snapshot.Marker)
	}
	listing := snapshot.Listings["en-us"]
	if listing.Title != "Title" || listing.Description != "Description" || compactJSON(t, listing.MinimumHardware) != `"Any PC"` {
		t.Fatalf("listing = %+v", listing)
	}
	encoded, err := json.Marshal(listing)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "releaseNotes") || strings.Contains(string(encoded), "privacyPolicy") || strings.Contains(string(encoded), "platformOverrides") {
		t.Fatalf("listing contains non-editable fields: %s", encoded)
	}
	entries := snapshot.Images.Images["en-us"]
	if len(entries) != 2 {
		t.Fatalf("manifest entries = %+v", entries)
	}
	if entries[0].LocalPath != "en-us/screen.png" || entries[0].RemoteOnly || entries[0].SHA256 == "" || entries[0].StoreID != "local" {
		t.Fatalf("local entry = %+v", entries[0])
	}
	if entries[1].LocalPath != "legacy-name.png" || !entries[1].RemoteOnly || entries[1].StoreID != "remote" {
		t.Fatalf("remote entry = %+v", entries[1])
	}
	if len(serverImages["en-us"]) != 2 || serverImages["en-us"][0].ID != "local" {
		t.Fatalf("server images = %+v", serverImages)
	}
}

func TestSnapshotFromSubmissionRejectsMissingBaseListing(t *testing.T) {
	t.Parallel()
	_, _, err := metadata.SnapshotFromSubmission(t.TempDir(), "APP", "pcenter test", time.Now(), json.RawMessage(`{
      "id":"submission","listings":{"en-us":{"platformOverrides":{}}}
    }`))
	if err == nil || !strings.Contains(err.Error(), "baseListing") {
		t.Fatalf("error = %v", err)
	}
}
