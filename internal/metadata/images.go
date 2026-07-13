package metadata

import (
	"errors"
	"fmt"
	"image/png"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxImageBytes int64 = 50 * 1024 * 1024

// ValidateImages validates manifest metadata and every referenced local image before Store mutation.
func ValidateImages(metadataDir string, manifest ImageManifest) error {
	hasScreenshot := false
	for rawLocale, entries := range manifest.Images {
		locale, err := canonicalLocale(rawLocale)
		if err != nil {
			return fmt.Errorf("image manifest locale %q: %w", rawLocale, err)
		}
		counts := make(map[string]int)
		for index, entry := range entries {
			if strings.TrimSpace(entry.ImageType) == "" {
				return fmt.Errorf("image manifest %q entry %d imageType is required", locale, index)
			}
			if utf8.RuneCountInString(entry.Description) > 200 {
				return fmt.Errorf("image manifest %q entry %d description exceeds 200 characters", locale, index)
			}
			if isScreenshotType(entry.ImageType) {
				hasScreenshot = true
				counts[entry.ImageType]++
				maximum := 8
				if entry.ImageType == "Screenshot" {
					maximum = 10
				}
				if counts[entry.ImageType] > maximum {
					return fmt.Errorf("image manifest %q has %d %s images; maximum is %d", locale, counts[entry.ImageType], entry.ImageType, maximum)
				}
			}
			if entry.RemoteOnly {
				if strings.TrimSpace(entry.StoreID) == "" {
					return fmt.Errorf("image manifest %q entry %d remote-only image requires storeId", locale, index)
				}
				continue
			}
			if err := validateLocalImage(metadataDir, locale, entry); err != nil {
				return fmt.Errorf("image manifest %q entry %d: %w", locale, index, err)
			}
		}
	}
	if !hasScreenshot {
		return errors.New("image manifest requires at least one screenshot")
	}
	return nil
}

func validateLocalImage(metadataDir, locale string, entry ImageEntry) error {
	clean := path.Clean(entry.LocalPath)
	prefix := locale + "/"
	if clean != entry.LocalPath || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || strings.Count(clean, "/") != 1 {
		return fmt.Errorf("localPath %q must be a safe locale-prefixed path", entry.LocalPath)
	}
	if !strings.HasPrefix(clean, prefix) {
		return fmt.Errorf("localPath %q must start with %s", entry.LocalPath, prefix)
	}
	if !strings.EqualFold(path.Ext(clean), ".png") {
		return fmt.Errorf("local image %q must be PNG", entry.LocalPath)
	}
	filePath := filepath.Join(metadataDir, "images", filepath.FromSlash(clean))
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("read local image %q: %w", entry.LocalPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("local image %q must be a regular file", entry.LocalPath)
	}
	if info.Size() > maxImageBytes {
		return fmt.Errorf("local image %q exceeds 50 MB", entry.LocalPath)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open local image %q: %w", entry.LocalPath, err)
	}
	config, decodeErr := png.DecodeConfig(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return fmt.Errorf("decode local image %q as PNG: %w", entry.LocalPath, decodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close local image %q: %w", entry.LocalPath, closeErr)
	}
	if entry.ImageType == "Screenshot" && (max(config.Width, config.Height) < 1366 || min(config.Width, config.Height) < 768) {
		return fmt.Errorf("desktop screenshot %q must be at least 1366x768 in landscape or portrait", entry.LocalPath)
	}
	if entry.ImageType == "Icon" && (config.Width != 300 || config.Height != 300) {
		return fmt.Errorf("icon %q must be 300x300", entry.LocalPath)
	}
	return nil
}

func isScreenshotType(imageType string) bool {
	return strings.HasSuffix(imageType, "Screenshot")
}
