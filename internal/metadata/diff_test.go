package metadata_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/metadata"
)

func TestDiffImagesRetainsRemoteOnlyAndUnmentionedServerImages(t *testing.T) {
	t.Parallel()
	server := map[string][]metadata.StoreImage{
		"EN-US": {
			storeImage(`{"fileName":"remote.png","fileStatus":"Uploaded","id":"remote","description":"Remote","imageType":"Screenshot","future":"preserved"}`),
			storeImage(`{"fileName":"unmentioned.png","fileStatus":"Uploaded","id":"unmentioned","description":"Other","imageType":"Screenshot"}`),
		},
	}
	manifest := metadata.ImageManifest{Images: map[string][]metadata.ImageEntry{
		"en-us": {{ImageType: "Screenshot", StoreID: "remote", RemoteOnly: true}},
	}}
	diff, err := metadata.DiffImages(t.TempDir(), manifest, server)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Changes) != 0 || len(diff.Uploads) != 0 {
		t.Fatalf("unexpected diff = %+v", diff)
	}
	updates := decodeUpdates(t, diff.Images["en-us"])
	if len(updates) != 2 || updates[0]["future"] != "preserved" || updates[1]["id"] != "unmentioned" {
		t.Fatalf("retained updates = %+v", updates)
	}
}

func TestDiffImagesAddsReplacesDeletesAndUpdatesCaptions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "images", "en-us", "stable.png"), 1366, 768)
	writePNG(t, filepath.Join(dir, "images", "en-us", "replace.png"), 1366, 768)
	writePNG(t, filepath.Join(dir, "images", "en-us", "new.png"), 1366, 768)
	stableHash := fileSHA256(t, filepath.Join(dir, "images", "en-us", "stable.png"))
	server := map[string][]metadata.StoreImage{"en-us": {
		storeImage(`{"fileName":"en-us/stable.png","fileStatus":"Uploaded","id":"stable","description":"Old caption","imageType":"Screenshot","future":1}`),
		storeImage(`{"fileName":"en-us/replace.png","fileStatus":"Uploaded","id":"replace","description":"Replace","imageType":"Screenshot","future":2}`),
		storeImage(`{"fileName":"delete.png","fileStatus":"Uploaded","id":"delete","description":"Delete","imageType":"Screenshot","future":3}`),
	}}
	manifest := metadata.ImageManifest{Images: map[string][]metadata.ImageEntry{"EN-US": {
		{LocalPath: "en-us/stable.png", ImageType: "Screenshot", Description: "New caption", StoreID: "stable", SHA256: stableHash},
		{LocalPath: "en-us/replace.png", ImageType: "Screenshot", Description: "Replacement", StoreID: "replace", SHA256: strings.Repeat("0", sha256.Size*2)},
		{LocalPath: "en-us/new.png", ImageType: "Screenshot", Description: "New"},
		{ImageType: "Screenshot", StoreID: "delete", Delete: true},
	}}}
	diff, err := metadata.DiffImages(dir, manifest, server)
	if err != nil {
		t.Fatal(err)
	}
	wantChanges := []metadata.ImageChange{
		{Locale: "en-us", Action: "add", LocalPath: "en-us/new.png"},
		{Locale: "en-us", Action: "caption", LocalPath: "en-us/stable.png", StoreID: "stable"},
		{Locale: "en-us", Action: "delete", StoreID: "delete"},
		{Locale: "en-us", Action: "replace", LocalPath: "en-us/replace.png", StoreID: "replace"},
	}
	if !reflect.DeepEqual(diff.Changes, wantChanges) {
		t.Fatalf("changes = %+v, want %+v", diff.Changes, wantChanges)
	}
	if len(diff.Uploads) != 2 || diff.Uploads[0].Name != "en-us/new.png" || diff.Uploads[1].Name != "en-us/replace.png" {
		t.Fatalf("uploads = %+v", diff.Uploads)
	}

	updates := decodeUpdates(t, diff.Images["en-us"])
	assertImageUpdate(t, updates, "stable", "en-us/stable.png", "Uploaded", "New caption", 1)
	assertImageUpdate(t, updates, "replace", "en-us/replace.png", "PendingDelete", "Replace", 2)
	assertImageUpdate(t, updates, "delete", "delete.png", "PendingDelete", "Delete", 3)
	assertImageUpdate(t, updates, "", "en-us/new.png", "PendingUpload", "New", nil)
	assertImageUpdate(t, updates, "", "en-us/replace.png", "PendingUpload", "Replacement", nil)
}

func TestDiffImagesRejectsManifestReferencesToMissingStoreImages(t *testing.T) {
	t.Parallel()
	manifest := metadata.ImageManifest{Images: map[string][]metadata.ImageEntry{
		"en-us": {{ImageType: "Screenshot", StoreID: "missing", RemoteOnly: true}},
	}}
	_, err := metadata.DiffImages(t.TempDir(), manifest, map[string][]metadata.StoreImage{"en-us": {}})
	if err == nil {
		t.Fatal("expected missing Store image error")
	}
}

func storeImage(raw string) metadata.StoreImage {
	var image metadata.StoreImage
	if err := json.Unmarshal([]byte(raw), &image); err != nil {
		panic(err)
	}
	image.Raw = json.RawMessage(raw)
	return image
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func decodeUpdates(t *testing.T, raw []json.RawMessage) []map[string]any {
	t.Helper()
	result := make([]map[string]any, len(raw))
	for index := range raw {
		if err := json.Unmarshal(raw[index], &result[index]); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func assertImageUpdate(t *testing.T, updates []map[string]any, id, fileName, status, description string, future any) {
	t.Helper()
	for _, update := range updates {
		idMatches := update["id"] == id || (id == "" && update["id"] == nil)
		if idMatches && update["fileName"] == fileName && update["fileStatus"] == status {
			if update["description"] != description {
				t.Fatalf("update description = %v, want %q", update["description"], description)
			}
			if future != nil && update["future"] != float64(future.(int)) {
				t.Fatalf("future field = %v, want %v", update["future"], future)
			}
			return
		}
	}
	t.Fatalf("missing image update id=%q file=%q status=%q in %+v", id, fileName, status, updates)
}
