package metadata_test

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/metadata"
)

func TestValidateImagesAcceptsLandscapePortraitAndRemoteOnlyScreenshots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "images", "en-us", "landscape.png"), 1366, 768)
	writePNG(t, filepath.Join(dir, "images", "en-us", "portrait.png"), 768, 1366)
	manifest := metadata.ImageManifest{Images: map[string][]metadata.ImageEntry{
		"EN-US": {
			{LocalPath: "en-us/landscape.png", ImageType: "Screenshot", Description: "Landscape"},
			{LocalPath: "en-us/portrait.png", ImageType: "Screenshot"},
		},
		"it": {{ImageType: "Screenshot", StoreID: "remote", RemoteOnly: true}},
	}}
	if err := metadata.ValidateImages(dir, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestValidateImagesRejectsInvalidFilesAndMetadata(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		entry   metadata.ImageEntry
		prepare func(*testing.T, string)
		wantErr string
	}{
		{
			name: "non png", entry: metadata.ImageEntry{LocalPath: "en-us/screen.jpg", ImageType: "Screenshot"},
			prepare: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, "images", "en-us", "screen.jpg"), "jpg")
			},
			wantErr: "must be PNG",
		},
		{
			name: "small desktop", entry: metadata.ImageEntry{LocalPath: "en-us/screen.png", ImageType: "Screenshot"},
			prepare: func(t *testing.T, dir string) {
				writePNG(t, filepath.Join(dir, "images", "en-us", "screen.png"), 1000, 700)
			},
			wantErr: "at least 1366x768",
		},
		{
			name: "wrong icon size", entry: metadata.ImageEntry{LocalPath: "en-us/icon.png", ImageType: "Icon"},
			prepare: func(t *testing.T, dir string) {
				writePNG(t, filepath.Join(dir, "images", "en-us", "icon.png"), 301, 300)
			},
			wantErr: "300x300",
		},
		{
			name: "caption too long", entry: metadata.ImageEntry{LocalPath: "en-us/screen.png", ImageType: "Screenshot", Description: strings.Repeat("é", 201)},
			prepare: func(t *testing.T, dir string) {
				writePNG(t, filepath.Join(dir, "images", "en-us", "screen.png"), 1366, 768)
			},
			wantErr: "200 characters",
		},
		{
			name: "unsafe path", entry: metadata.ImageEntry{LocalPath: "../screen.png", ImageType: "Screenshot"},
			prepare: func(*testing.T, string) {}, wantErr: "locale-prefixed",
		},
		{
			name: "wrong locale prefix", entry: metadata.ImageEntry{LocalPath: "it/screen.png", ImageType: "Screenshot"},
			prepare: func(*testing.T, string) {}, wantErr: "must start with en-us/",
		},
		{
			name: "oversize", entry: metadata.ImageEntry{LocalPath: "en-us/screen.png", ImageType: "Screenshot"},
			prepare: func(t *testing.T, dir string) {
				path := filepath.Join(dir, "images", "en-us", "screen.png")
				writePNG(t, path, 1366, 768)
				if err := os.Truncate(path, 50*1024*1024+1); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "50 MB",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			testCase.prepare(t, dir)
			manifest := metadata.ImageManifest{Images: map[string][]metadata.ImageEntry{"en-us": {testCase.entry}}}
			err := metadata.ValidateImages(dir, manifest)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, testCase.wantErr)
			}
		})
	}
}

func TestValidateImagesEnforcesCountsAndRequiresAScreenshot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	desktop := make([]metadata.ImageEntry, 11)
	for index := range desktop {
		desktop[index] = metadata.ImageEntry{ImageType: "Screenshot", StoreID: "remote", RemoteOnly: true}
	}
	err := metadata.ValidateImages(dir, metadata.ImageManifest{Images: map[string][]metadata.ImageEntry{"en-us": desktop}})
	if err == nil || !strings.Contains(err.Error(), "maximum is 10") {
		t.Fatalf("desktop count error = %v", err)
	}
	other := make([]metadata.ImageEntry, 9)
	for index := range other {
		other[index] = metadata.ImageEntry{ImageType: "MobileScreenshot", StoreID: "remote", RemoteOnly: true}
	}
	err = metadata.ValidateImages(dir, metadata.ImageManifest{Images: map[string][]metadata.ImageEntry{"en-us": other}})
	if err == nil || !strings.Contains(err.Error(), "maximum is 8") {
		t.Fatalf("other count error = %v", err)
	}
	err = metadata.ValidateImages(dir, metadata.ImageManifest{Images: map[string][]metadata.ImageEntry{
		"en-us": {{ImageType: "Icon", StoreID: "remote", RemoteOnly: true}},
	}})
	if err == nil || !strings.Contains(err.Error(), "at least one screenshot") {
		t.Fatalf("screenshot error = %v", err)
	}
}

func writePNG(t *testing.T, path string, width, height int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}
