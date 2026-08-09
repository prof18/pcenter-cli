package metadata_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prof18/pcenter-cli/internal/metadata"
)

func TestWriteAndLoadSnapshotCanonicalizesLocalesAndPreservesRawHardware(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pulledAt := time.Date(2026, time.July, 14, 10, 30, 0, 0, time.UTC)
	snapshot := metadata.Snapshot{
		Marker: metadata.StoreMarker{
			AppID: "APP", PulledAt: pulledAt, SourceSubmissionID: "submission", GeneratedBy: "pcenter test",
		},
		Listings: map[string]metadata.Listing{
			"EN-us": {
				Title: "Title", Description: "Description", Features: []string{"One"}, Keywords: []string{"rss"},
				CopyrightAndTrademarkInfo: "Copyright", LicenseTerms: "Terms",
				RecommendedHardware: json.RawMessage(`["Keyboard"]`),
				MinimumHardware:     json.RawMessage(`"Any compatible PC"`),
			},
		},
		Images: metadata.ImageManifest{Images: map[string][]metadata.ImageEntry{}},
	}
	if err := metadata.WriteSnapshot(dir, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "listings", "en-us.json")); err != nil {
		t.Fatalf("canonical listing file missing: %v", err)
	}
	loaded, err := metadata.LoadDirectory(dir, "APP")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Marker.PulledAt != pulledAt || loaded.Marker.SourceSubmissionID != "submission" {
		t.Fatalf("marker = %+v", loaded.Marker)
	}
	listing := loaded.Listings["en-us"]
	if compactJSON(t, listing.RecommendedHardware) != `["Keyboard"]` || compactJSON(t, listing.MinimumHardware) != `"Any compatible PC"` {
		t.Fatalf("hardware fields = %s / %s", listing.RecommendedHardware, listing.MinimumHardware)
	}
	if _, err := os.Stat(filepath.Join(dir, "images-manifest.json")); err != nil {
		t.Fatalf("images manifest missing: %v", err)
	}
}

func TestLoadDirectoryEnforcesIdentityMarker(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		marker  string
		wantErr string
	}{
		{name: "missing", wantErr: "store.json is required"},
		{name: "wrong app", marker: `{"appId":"OTHER"}`, wantErr: "belongs to app OTHER"},
		{name: "empty app", marker: `{"appId":""}`, wantErr: "appId is required"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			if testCase.marker != "" {
				if err := os.WriteFile(filepath.Join(dir, "store.json"), []byte(testCase.marker), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := metadata.LoadDirectory(dir, "APP")
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, testCase.wantErr)
			}
		})
	}
}

func TestWriteSnapshotRejectsCaseInsensitiveDuplicateLocales(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	err := metadata.WriteSnapshot(dir, metadata.Snapshot{
		Marker: metadata.StoreMarker{AppID: "APP"},
		Listings: map[string]metadata.Listing{
			"en-us": {Title: "one"},
			"EN-US": {Title: "two"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicates locale") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteSnapshotRemovesStaleListingFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	listingsDir := filepath.Join(dir, "listings")
	if err := os.MkdirAll(listingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(listingsDir, "stale.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(listingsDir, "notes.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := metadata.WriteSnapshot(dir, metadata.Snapshot{
		Marker: metadata.StoreMarker{AppID: "APP"},
		Listings: map[string]metadata.Listing{
			"en-US": {Title: "Title"},
		},
		Images: metadata.ImageManifest{Images: map[string][]metadata.ImageEntry{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(listingsDir, "stale.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale listing still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(listingsDir, "notes.txt")); err != nil {
		t.Fatalf("non-listing file was removed: %v", err)
	}
}

func TestValidateListingsEnforcesOnlyApprovedCollectionLimits(t *testing.T) {
	t.Parallel()
	valid := metadata.Listing{
		Features:            make([]string, 20),
		RecommendedHardware: json.RawMessage(`[` + strings.Repeat(`"item",`, 10) + `"item"]`),
		MinimumHardware:     json.RawMessage(`"server string shape"`),
	}
	if err := metadata.ValidateListings(map[string]metadata.Listing{"en-us": valid}); err != nil {
		t.Fatal(err)
	}
	invalidFeatures := valid
	invalidFeatures.Features = make([]string, 21)
	if err := metadata.ValidateListings(map[string]metadata.Listing{"en-us": invalidFeatures}); err == nil || !strings.Contains(err.Error(), "features") {
		t.Fatalf("features error = %v", err)
	}
	invalidHardware := valid
	invalidHardware.MinimumHardware = json.RawMessage(`[` + strings.Repeat(`"item",`, 11) + `"item"]`)
	if err := metadata.ValidateListings(map[string]metadata.Listing{"en-us": invalidHardware}); err == nil || !strings.Contains(err.Error(), "minimumHardware") {
		t.Fatalf("hardware error = %v", err)
	}
}

func TestValidateLocaleRemovalIsCaseInsensitiveAndGuarded(t *testing.T) {
	t.Parallel()
	local := map[string]metadata.Listing{"en-us": {}, "IT": {}}
	_, err := metadata.ValidateLocaleRemoval([]string{"EN-US", "it", "fr"}, local, false)
	if err == nil || !strings.Contains(err.Error(), "fr") {
		t.Fatalf("error = %v", err)
	}
	removed, err := metadata.ValidateLocaleRemoval([]string{"EN-US", "it", "fr"}, local, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "fr" {
		t.Fatalf("removed = %v", removed)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	return compact.String()
}

// Both limits below are enforced here rather than left to the API: a 400 from
// Ingestion arrives only after a submission has been created, which then has to
// be cleaned up.
func TestValidateListingsRejectsOverLongShortDescription(t *testing.T) {
	t.Parallel()
	err := metadata.ValidateListings(map[string]metadata.Listing{
		"en-us": {ShortDescription: strings.Repeat("a", 501)},
	})
	if err == nil || !strings.Contains(err.Error(), "shortDescription is 501 characters") {
		t.Fatalf("err = %v, want the 500-character limit reported", err)
	}
	// 500 exactly is fine, and the limit counts runes rather than bytes.
	if err := metadata.ValidateListings(map[string]metadata.Listing{
		"en-us": {ShortDescription: strings.Repeat("ä", 500)},
	}); err != nil {
		t.Fatalf("500 runes should be accepted: %v", err)
	}
}

func TestValidateListingsRejectsTooManyKeywordCarryingLocales(t *testing.T) {
	t.Parallel()
	// Each locale is individually fine; only the number of locales carrying
	// keywords at all is capped, so this cannot be caught per locale.
	listings := make(map[string]metadata.Listing, 22)
	for index := range 22 {
		listings[fmt.Sprintf("l%02d", index)] = metadata.Listing{Keywords: []string{"rss"}}
	}
	err := metadata.ValidateListings(listings)
	if err == nil || !strings.Contains(err.Error(), "22 locales carry keywords") {
		t.Fatalf("err = %v, want the keyword-locale cap reported", err)
	}

	delete(listings, "l21")
	if err := metadata.ValidateListings(listings); err != nil {
		t.Fatalf("21 keyword locales should be accepted: %v", err)
	}
}
