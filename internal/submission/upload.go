package submission

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"

	"github.com/prof18/pcenter-cli/internal/store"
)

const uploadMinimumRate = 200 * 1024

// ArchiveEntry maps a local source file to its ZIP-internal Store file name.
type ArchiveEntry struct {
	SourcePath string
	Name       string
}

// BlobUploader uploads a prepared ZIP to a credential-bearing Store SAS URL.
type BlobUploader interface {
	Upload(context.Context, string, string) error
}

// AzureBlobUploader uploads through the Azure SDK's block-blob implementation.
type AzureBlobUploader struct{}

// UploadError is a sanitized blob error that retains the response status for recovery logic.
type UploadError struct {
	StatusCode int
	URL        string
	Message    string
}

func (e *UploadError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("Azure blob upload to %s failed with HTTP %d: %s", e.URL, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("Azure blob upload to %s failed: %s", e.URL, e.Message)
}

// NewAzureBlobUploader creates the production SDK-backed uploader.
func NewAzureBlobUploader() *AzureBlobUploader { return &AzureBlobUploader{} }

// CreateUploadZIP writes uncompressed, reproducible ZIP entries for Store ingestion.
func CreateUploadZIP(destination string, entries []ArchiveEntry) (returnErr error) {
	if len(entries) == 0 {
		return errors.New("at least one ZIP entry is required")
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := validateArchiveEntry(entry, seen); err != nil {
			return err
		}
	}

	file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create upload ZIP: %w", err)
	}
	archive := zip.NewWriter(file)
	defer func() {
		if returnErr != nil {
			_ = os.Remove(destination)
		}
	}()
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Store}
		header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		writer, createErr := archive.CreateHeader(header)
		if createErr != nil {
			_ = archive.Close()
			_ = file.Close()
			return fmt.Errorf("create ZIP entry %q: %w", entry.Name, createErr)
		}
		source, openErr := os.Open(entry.SourcePath)
		if openErr != nil {
			_ = archive.Close()
			_ = file.Close()
			return fmt.Errorf("open ZIP source %q: %w", entry.SourcePath, openErr)
		}
		_, copyErr := io.Copy(writer, source)
		closeErr := source.Close()
		if copyErr != nil {
			_ = archive.Close()
			_ = file.Close()
			return fmt.Errorf("write ZIP entry %q: %w", entry.Name, copyErr)
		}
		if closeErr != nil {
			_ = archive.Close()
			_ = file.Close()
			return fmt.Errorf("close ZIP source %q: %w", entry.SourcePath, closeErr)
		}
	}
	if err := archive.Close(); err != nil {
		_ = file.Close()
		return fmt.Errorf("finalize upload ZIP: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close upload ZIP: %w", err)
	}
	return nil
}

// ReencodeSASPlus preserves literal plus characters in the SAS query as %2B.
func ReencodeSASPlus(rawURL string) string {
	queryIndex := strings.IndexByte(rawURL, '?')
	if queryIndex < 0 {
		return rawURL
	}
	return rawURL[:queryIndex+1] + strings.ReplaceAll(rawURL[queryIndex+1:], "+", "%2B")
}

// UploadTimeout scales the overall bound to 200 KiB/s with a ten-minute floor.
func UploadTimeout(size int64) time.Duration {
	if size <= 0 {
		return 10 * time.Minute
	}
	scaled := time.Duration(size) * time.Second / uploadMinimumRate
	if scaled < 10*time.Minute {
		return 10 * time.Minute
	}
	return scaled
}

// Upload sends a ZIP with chunking, parallelism, and the locked blob retry schedule.
func (u *AzureBlobUploader) Upload(ctx context.Context, fileUploadURL, zipPath string) error {
	file, err := os.Open(zipPath)
	if err != nil {
		return fmt.Errorf("open upload ZIP: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat upload ZIP: %w", err)
	}
	uploadCtx, cancel := context.WithTimeout(ctx, UploadTimeout(info.Size()))
	defer cancel()

	encodedURL := ReencodeSASPlus(fileUploadURL)
	client, err := blockblob.NewClientWithNoCredential(encodedURL, &blockblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Retry: policy.RetryOptions{
				MaxRetries:    3,
				RetryDelay:    15 * time.Second,
				MaxRetryDelay: 120 * time.Second,
			},
		},
	})
	if err != nil {
		return sanitizedUploadError(encodedURL, err)
	}
	contentType := "application/zip"
	_, err = client.UploadFile(uploadCtx, file, &blockblob.UploadFileOptions{
		BlockSize:   8 * 1024 * 1024,
		Concurrency: 4,
		HTTPHeaders: &blob.HTTPHeaders{BlobContentType: &contentType},
	})
	if err != nil {
		return sanitizedUploadError(encodedURL, err)
	}
	return nil
}

// IsForbiddenUploadError identifies an expired or rejected SAS response.
func IsForbiddenUploadError(err error) bool {
	var uploadError *UploadError
	if errors.As(err, &uploadError) {
		return uploadError.StatusCode == http.StatusForbidden
	}
	var responseError *azcore.ResponseError
	return errors.As(err, &responseError) && responseError.StatusCode == http.StatusForbidden
}

func validateArchiveEntry(entry ArchiveEntry, seen map[string]struct{}) error {
	if strings.TrimSpace(entry.SourcePath) == "" {
		return errors.New("ZIP source path is required")
	}
	if entry.Name == "" || strings.HasPrefix(entry.Name, "/") || strings.Contains(entry.Name, "\\") || path.Clean(entry.Name) != entry.Name || strings.HasPrefix(entry.Name, "../") {
		return fmt.Errorf("unsafe ZIP entry name %q", entry.Name)
	}
	if _, duplicate := seen[entry.Name]; duplicate {
		return fmt.Errorf("duplicate ZIP entry name %q", entry.Name)
	}
	seen[entry.Name] = struct{}{}
	info, err := os.Stat(entry.SourcePath)
	if err != nil {
		return fmt.Errorf("stat ZIP source %q: %w", entry.SourcePath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("ZIP source %q is not a regular file", entry.SourcePath)
	}
	return nil
}

func sanitizedUploadError(rawURL string, err error) error {
	statusCode := 0
	var responseError *azcore.ResponseError
	if errors.As(err, &responseError) {
		statusCode = responseError.StatusCode
	}
	redactor := store.NewRedactor()
	return &UploadError{
		StatusCode: statusCode,
		URL:        redactor.Redact(rawURL),
		Message:    redactor.Redact(err.Error()),
	}
}
