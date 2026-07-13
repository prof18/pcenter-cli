package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SnapshotFromSubmission extracts the canonical editable metadata from raw submission JSON.
func SnapshotFromSubmission(metadataDir, appID, generatedBy string, pulledAt time.Time, submissionJSON json.RawMessage) (Snapshot, map[string][]StoreImage, error) {
	var submission map[string]json.RawMessage
	if err := json.Unmarshal(submissionJSON, &submission); err != nil {
		return Snapshot{}, nil, fmt.Errorf("decode submission: %w", err)
	}
	var submissionID string
	if err := json.Unmarshal(submission["id"], &submissionID); err != nil || submissionID == "" {
		return Snapshot{}, nil, errors.New("submission id is required")
	}
	var rawListings map[string]json.RawMessage
	if err := json.Unmarshal(submission["listings"], &rawListings); err != nil {
		return Snapshot{}, nil, fmt.Errorf("decode submission listings: %w", err)
	}
	if len(rawListings) == 0 {
		return Snapshot{}, nil, errors.New("submission contains no listings")
	}
	snapshot := Snapshot{
		Marker:   StoreMarker{AppID: appID, PulledAt: pulledAt.UTC(), SourceSubmissionID: submissionID, GeneratedBy: generatedBy},
		Listings: make(map[string]Listing, len(rawListings)),
		Images:   ImageManifest{Images: make(map[string][]ImageEntry, len(rawListings))},
	}
	serverImages := make(map[string][]StoreImage, len(rawListings))
	for rawLocale, rawListing := range rawListings {
		locale, err := canonicalLocale(rawLocale)
		if err != nil {
			return Snapshot{}, nil, err
		}
		if _, duplicate := snapshot.Listings[locale]; duplicate {
			return Snapshot{}, nil, fmt.Errorf("submission listing locale %q is duplicated case-insensitively", rawLocale)
		}
		listing, images, entries, err := extractListing(metadataDir, locale, rawListing)
		if err != nil {
			return Snapshot{}, nil, err
		}
		snapshot.Listings[locale] = listing
		snapshot.Images.Images[locale] = entries
		serverImages[locale] = images
	}
	if err := ValidateListings(snapshot.Listings); err != nil {
		return Snapshot{}, nil, err
	}
	return snapshot, serverImages, nil
}

func extractListing(metadataDir, locale string, rawListing json.RawMessage) (Listing, []StoreImage, []ImageEntry, error) {
	var listingObject map[string]json.RawMessage
	if err := json.Unmarshal(rawListing, &listingObject); err != nil {
		return Listing{}, nil, nil, fmt.Errorf("decode Store listing %q: %w", locale, err)
	}
	rawBase, exists := listingObject["baseListing"]
	if !exists || string(rawBase) == "null" {
		return Listing{}, nil, nil, fmt.Errorf("store listing %q does not contain a baseListing", locale)
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal(rawBase, &base); err != nil {
		return Listing{}, nil, nil, fmt.Errorf("decode Store listing %q baseListing: %w", locale, err)
	}
	listing := Listing{Features: []string{}, Keywords: []string{}}
	if err := decodeStringField(base, "title", &listing.Title); err != nil {
		return Listing{}, nil, nil, fieldError(locale, err)
	}
	if err := decodeStringField(base, "description", &listing.Description); err != nil {
		return Listing{}, nil, nil, fieldError(locale, err)
	}
	if err := decodeStringsField(base, "features", &listing.Features); err != nil {
		return Listing{}, nil, nil, fieldError(locale, err)
	}
	if err := decodeStringsField(base, "keywords", &listing.Keywords); err != nil {
		return Listing{}, nil, nil, fieldError(locale, err)
	}
	if err := decodeStringField(base, "copyrightAndTrademarkInfo", &listing.CopyrightAndTrademarkInfo); err != nil {
		return Listing{}, nil, nil, fieldError(locale, err)
	}
	if err := decodeStringField(base, "licenseTerms", &listing.LicenseTerms); err != nil {
		return Listing{}, nil, nil, fieldError(locale, err)
	}
	listing.RecommendedHardware = cloneRaw(base["recommendedHardware"])
	listing.MinimumHardware = cloneRaw(base["minimumHardware"])
	images, entries, err := extractImages(metadataDir, locale, base["images"])
	if err != nil {
		return Listing{}, nil, nil, err
	}
	return listing, images, entries, nil
}

func extractImages(metadataDir, locale string, raw json.RawMessage) ([]StoreImage, []ImageEntry, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []StoreImage{}, []ImageEntry{}, nil
	}
	var rawImages []json.RawMessage
	if err := json.Unmarshal(raw, &rawImages); err != nil {
		return nil, nil, fmt.Errorf("decode Store listing %q images: %w", locale, err)
	}
	images := make([]StoreImage, 0, len(rawImages))
	entries := make([]ImageEntry, 0, len(rawImages))
	for index, rawImage := range rawImages {
		var image StoreImage
		if err := json.Unmarshal(rawImage, &image); err != nil {
			return nil, nil, fmt.Errorf("decode Store listing %q image %d: %w", locale, index, err)
		}
		image.Raw = cloneRaw(rawImage)
		images = append(images, image)
		entry := ImageEntry{
			LocalPath: image.FileName, ImageType: image.ImageType, Description: image.Description,
			StoreID: image.ID, RemoteOnly: true,
		}
		if isMatchableLocalImage(locale, image.FileName) {
			localPath := filepath.Join(metadataDir, "images", filepath.FromSlash(image.FileName))
			if info, statErr := os.Stat(localPath); statErr == nil && info.Mode().IsRegular() {
				hash, hashErr := imageSHA256(metadataDir, image.FileName)
				if hashErr != nil {
					return nil, nil, hashErr
				}
				entry.RemoteOnly = false
				entry.SHA256 = hash
			} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return nil, nil, fmt.Errorf("inspect local image %q: %w", image.FileName, statErr)
			}
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(left, right int) bool {
		leftKey := entries[left].LocalPath + "\x00" + entries[left].StoreID
		rightKey := entries[right].LocalPath + "\x00" + entries[right].StoreID
		return leftKey < rightKey
	})
	return images, entries, nil
}

func isMatchableLocalImage(locale, fileName string) bool {
	clean := path.Clean(fileName)
	return clean == fileName && strings.HasPrefix(clean, locale+"/") && strings.Count(clean, "/") == 1
}

func decodeStringField(source map[string]json.RawMessage, field string, destination *string) error {
	raw, exists := source[field]
	if !exists || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("field %s must be a string: %w", field, err)
	}
	return nil
}

func decodeStringsField(source map[string]json.RawMessage, field string, destination *[]string) error {
	raw, exists := source[field]
	if !exists || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("field %s must be an array of strings: %w", field, err)
	}
	return nil
}

func fieldError(locale string, err error) error {
	return fmt.Errorf("store listing %q %w", locale, err)
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
