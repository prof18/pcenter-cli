// Package metadata implements the repository-backed Microsoft Store listing format.
package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	storeFileName    = "store.json"
	manifestFileName = "images-manifest.json"
)

// StoreMarker prevents metadata from being pushed to the wrong application.
type StoreMarker struct {
	AppID              string    `json:"appId"`
	PulledAt           time.Time `json:"pulledAt"`
	SourceSubmissionID string    `json:"sourceSubmissionId"`
	GeneratedBy        string    `json:"generatedBy"`
}

// Listing contains only the editable base-listing fields in the directory contract.
type Listing struct {
	Title                     string          `json:"title"`
	Description               string          `json:"description"`
	Features                  []string        `json:"features"`
	Keywords                  []string        `json:"keywords"`
	CopyrightAndTrademarkInfo string          `json:"copyrightAndTrademarkInfo"`
	LicenseTerms              string          `json:"licenseTerms"`
	RecommendedHardware       json.RawMessage `json:"recommendedHardware"`
	MinimumHardware           json.RawMessage `json:"minimumHardware"`
}

// ImageManifest records the local-to-Store image mapping. Image behavior is implemented separately.
type ImageManifest struct {
	Images map[string][]ImageEntry `json:"images"`
}

// ImageEntry is one image mapping in images-manifest.json.
type ImageEntry struct {
	LocalPath   string `json:"localPath,omitempty"`
	ImageType   string `json:"imageType"`
	Description string `json:"description,omitempty"`
	StoreID     string `json:"storeId,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	RemoteOnly  bool   `json:"remoteOnly,omitempty"`
}

// Snapshot is the complete on-disk metadata representation.
type Snapshot struct {
	Marker   StoreMarker
	Listings map[string]Listing
	Images   ImageManifest
}

// Directory is a validated metadata directory ready for diffing or push.
type Directory = Snapshot

// WriteSnapshot writes a canonical metadata snapshot.
func WriteSnapshot(dir string, snapshot Snapshot) error {
	if strings.TrimSpace(snapshot.Marker.AppID) == "" {
		return errors.New("store marker appId is required")
	}
	if len(snapshot.Listings) == 0 {
		return errors.New("at least one listing is required")
	}
	if err := ValidateListings(snapshot.Listings); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "listings"), 0o755); err != nil {
		return fmt.Errorf("create listings directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		return fmt.Errorf("create images directory: %w", err)
	}
	if err := writeJSON(filepath.Join(dir, storeFileName), snapshot.Marker); err != nil {
		return err
	}

	locales := make([]string, 0, len(snapshot.Listings))
	canonical := make(map[string]Listing, len(snapshot.Listings))
	for locale, listing := range snapshot.Listings {
		key, err := canonicalLocale(locale)
		if err != nil {
			return err
		}
		if _, duplicate := canonical[key]; duplicate {
			return fmt.Errorf("listing locale %q duplicates locale %q case-insensitively", locale, key)
		}
		canonical[key] = listing
		locales = append(locales, key)
	}
	sort.Strings(locales)
	for _, locale := range locales {
		if err := writeJSON(filepath.Join(dir, "listings", locale+".json"), canonical[locale]); err != nil {
			return err
		}
	}
	if snapshot.Images.Images == nil {
		snapshot.Images.Images = map[string][]ImageEntry{}
	}
	if err := writeJSON(filepath.Join(dir, manifestFileName), snapshot.Images); err != nil {
		return err
	}
	return nil
}

// LoadDirectory reads and validates a metadata directory for the expected application.
func LoadDirectory(dir, expectedAppID string) (Directory, error) {
	var result Directory
	if err := readJSON(filepath.Join(dir, storeFileName), &result.Marker); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Directory{}, errors.New("store.json is required; run listing pull for this app first")
		}
		return Directory{}, err
	}
	if strings.TrimSpace(result.Marker.AppID) == "" {
		return Directory{}, errors.New("store.json appId is required")
	}
	if result.Marker.AppID != expectedAppID {
		return Directory{}, fmt.Errorf("metadata directory belongs to app %s, not %s", result.Marker.AppID, expectedAppID)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "listings"))
	if err != nil {
		return Directory{}, fmt.Errorf("read listings directory: %w", err)
	}
	result.Listings = make(map[string]Listing)
	originalNames := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		locale, err := canonicalLocale(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		if err != nil {
			return Directory{}, fmt.Errorf("listing file %q: %w", entry.Name(), err)
		}
		if original, duplicate := originalNames[locale]; duplicate {
			return Directory{}, fmt.Errorf("listing file %q duplicates locale from %q case-insensitively", entry.Name(), original)
		}
		var listing Listing
		if err := readJSON(filepath.Join(dir, "listings", entry.Name()), &listing); err != nil {
			return Directory{}, err
		}
		originalNames[locale] = entry.Name()
		result.Listings[locale] = listing
	}
	if len(result.Listings) == 0 {
		return Directory{}, errors.New("metadata directory contains no listing files")
	}
	if err := ValidateListings(result.Listings); err != nil {
		return Directory{}, err
	}
	if err := readJSON(filepath.Join(dir, manifestFileName), &result.Images); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Directory{}, errors.New("images-manifest.json is required; run listing pull for this app first")
		}
		return Directory{}, err
	}
	if result.Images.Images == nil {
		result.Images.Images = map[string][]ImageEntry{}
	}
	return result, nil
}

// ValidateListings enforces the client-side limits fixed by the metadata contract.
func ValidateListings(listings map[string]Listing) error {
	for locale, listing := range listings {
		if len(listing.Features) > 20 {
			return fmt.Errorf("listing %q features has %d items; maximum is 20", locale, len(listing.Features))
		}
		if err := validateHardware(locale, "recommendedHardware", listing.RecommendedHardware); err != nil {
			return err
		}
		if err := validateHardware(locale, "minimumHardware", listing.MinimumHardware); err != nil {
			return err
		}
	}
	return nil
}

// ValidateLocaleRemoval rejects missing server locales unless removal was explicitly allowed.
func ValidateLocaleRemoval(serverLocales []string, local map[string]Listing, allowRemoval bool) ([]string, error) {
	locales := make(map[string]struct{}, len(local))
	for locale := range local {
		locales[strings.ToLower(locale)] = struct{}{}
	}
	removed := make([]string, 0)
	for _, locale := range serverLocales {
		if _, exists := locales[strings.ToLower(locale)]; !exists {
			removed = append(removed, locale)
		}
	}
	sort.Strings(removed)
	if len(removed) > 0 && !allowRemoval {
		return nil, fmt.Errorf("local metadata is missing Store locale(s) %s; pass --allow-locale-removal to remove them", strings.Join(removed, ", "))
	}
	return removed, nil
}

func validateHardware(locale, field string, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("listing %q %s is invalid JSON: %w", locale, field, err)
	}
	if items, ok := value.([]any); ok && len(items) > 11 {
		return fmt.Errorf("listing %q %s has %d items; maximum is 11", locale, field, len(items))
	}
	return nil
}

func canonicalLocale(locale string) (string, error) {
	locale = strings.TrimSpace(locale)
	if locale == "" || strings.ContainsAny(locale, `/\\`) || locale == "." || locale == ".." {
		return "", fmt.Errorf("invalid listing locale %q", locale)
	}
	return strings.ToLower(locale), nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func readJSON(path string, destination any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, destination); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
