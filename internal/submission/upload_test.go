package submission_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prof18/pcenter-cli/internal/submission"
)

func TestCreateUploadZIPUsesStoreEntriesAndRequestedNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	packagePath := filepath.Join(dir, "FeedFlow.msix")
	imagePath := filepath.Join(dir, "screen.png")
	if err := os.WriteFile(packagePath, []byte("msix-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, []byte("png-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(dir, "upload.zip")
	if err := submission.CreateUploadZIP(zipPath, []submission.ArchiveEntry{
		{SourcePath: packagePath, Name: "FeedFlow.msix"},
		{SourcePath: imagePath, Name: "en-us/screenshot-01.png"},
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if len(reader.File) != 2 {
		t.Fatalf("ZIP entries = %d", len(reader.File))
	}
	for _, file := range reader.File {
		if file.Method != zip.Store {
			t.Fatalf("entry %s method = %d, want Store", file.Name, file.Method)
		}
	}
	if reader.File[1].Name != "en-us/screenshot-01.png" {
		t.Fatalf("image entry name = %q", reader.File[1].Name)
	}
}

func TestCreateUploadZIPRejectsUnsafeOrDuplicateNames(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, entries := range [][]submission.ArchiveEntry{
		{{SourcePath: file, Name: "../escape"}},
		{{SourcePath: file, Name: "/absolute"}},
		{{SourcePath: file, Name: "same"}, {SourcePath: file, Name: "same"}},
	} {
		if err := submission.CreateUploadZIP(filepath.Join(t.TempDir(), "upload.zip"), entries); err == nil {
			t.Fatalf("unsafe entries accepted: %+v", entries)
		}
	}
}

func TestReencodeSASPlusOnlyTouchesLiteralQueryPluses(t *testing.T) {
	t.Parallel()
	input := "https://blob.example/container/a+b.zip?sv=1&sig=abc+def%2Bghi"
	want := "https://blob.example/container/a+b.zip?sv=1&sig=abc%2Bdef%2Bghi"
	if got := submission.ReencodeSASPlus(input); got != want {
		t.Fatalf("ReencodeSASPlus = %q, want %q", got, want)
	}
}

func TestUploadTimeoutScalesFromTenMinuteFloor(t *testing.T) {
	t.Parallel()
	if got := submission.UploadTimeout(1); got != 10*time.Minute {
		t.Fatalf("small timeout = %s", got)
	}
	large := int64(200 * 1024 * 60 * 20)
	if got := submission.UploadTimeout(large); got != 20*time.Minute {
		t.Fatalf("large timeout = %s", got)
	}
}

func TestAzureBlobUploaderUploadsZIPAndReencodesPlus(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var rawQuery, blobContentType string
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		rawQuery = request.URL.RawQuery
		blobContentType = request.Header.Get("x-ms-blob-content-type")
		uploaded, _ = io.ReadAll(request.Body)
		mu.Unlock()
		w.Header().Set("ETag", `"etag"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "payload")
	if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(dir, "upload.zip")
	if err := submission.CreateUploadZIP(zipPath, []submission.ArchiveEntry{{SourcePath: file, Name: "payload"}}); err != nil {
		t.Fatal(err)
	}
	uploader := submission.NewAzureBlobUploader()
	if err := uploader.Upload(context.Background(), server.URL+"/container/upload.zip?sig=a+b", zipPath); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(rawQuery, "sig=a%2Bb") {
		t.Fatalf("raw query = %q", rawQuery)
	}
	if blobContentType != "application/zip" {
		t.Fatalf("blob content type = %q", blobContentType)
	}
	if !bytes.HasPrefix(uploaded, []byte("PK")) {
		t.Fatalf("uploaded body is not ZIP: %q", uploaded)
	}
}

func TestAzureBlobUploaderClassifiesForbidden(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "expired SAS")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "upload.zip")
	if err := os.WriteFile(path, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := submission.NewAzureBlobUploader().Upload(context.Background(), server.URL+"/blob?sig=x", path)
	if err == nil || !submission.IsForbiddenUploadError(err) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "sig=") || strings.Contains(err.Error(), "?sig=x") {
		t.Fatalf("upload error leaked SAS query: %v", err)
	}
}
