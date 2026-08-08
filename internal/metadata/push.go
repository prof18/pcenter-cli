package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

var listingPushAllowlist = []string{
	"applicationCategory", "pricing", "visibility", "targetPublishDate", "listings",
	"hardwarePreferences", "automaticBackupEnabled", "canInstallOnRemovableMedia",
	"isGameDvrEnabled", "gamingOptions", "hasExternalInAppProducts",
	"meetAccessibilityGuidelines", "notesForCertification", "enterpriseLicensing",
	"allowMicrosoftDecideAppAvailabilityToFutureDeviceFamilies",
	"allowTargetFutureDeviceFamilies", "trailers",
}

// ListingChange is one stable, user-facing listing text or locale change.
type ListingChange struct {
	Locale string `json:"locale"`
	Action string `json:"action"`
	Field  string `json:"field,omitempty"`
}

// PushPlan contains the complete listing PUT body and files required for upload.
type PushPlan struct {
	Body           json.RawMessage `json:"body"`
	ListingChanges []ListingChange `json:"listingChanges"`
	ImageChanges   []ImageChange   `json:"imageChanges"`
	Uploads        []ImageUpload   `json:"-"`
}

// HasChanges reports whether the directory differs from the source submission.
func (p PushPlan) HasChanges() bool {
	return len(p.ListingChanges) > 0 || len(p.ImageChanges) > 0
}

// BuildPushPlan applies a validated metadata directory to a full Store submission.
func BuildPushPlan(metadataDir string, directory Directory, submissionJSON json.RawMessage, allowLocaleRemoval bool) (PushPlan, error) {
	if err := ValidateListings(directory.Listings); err != nil {
		return PushPlan{}, err
	}
	serverSnapshot, serverImages, err := SnapshotFromSubmission(metadataDir, directory.Marker.AppID, "push-plan", directory.Marker.PulledAt, submissionJSON)
	if err != nil {
		return PushPlan{}, err
	}
	serverLocales := make([]string, 0, len(serverSnapshot.Listings))
	for locale := range serverSnapshot.Listings {
		serverLocales = append(serverLocales, locale)
	}
	if _, err := ValidateLocaleRemoval(serverLocales, directory.Listings, allowLocaleRemoval); err != nil {
		return PushPlan{}, err
	}
	imageDiff, err := DiffImages(metadataDir, directory.Images, serverImages)
	if err != nil {
		return PushPlan{}, err
	}

	var source map[string]json.RawMessage
	if err := json.Unmarshal(submissionJSON, &source); err != nil {
		return PushPlan{}, fmt.Errorf("decode submission JSON: %w", err)
	}
	rawListings, originalKeys, err := decodeRawListings(source["listings"])
	if err != nil {
		return PushPlan{}, err
	}
	localListings, err := canonicalListings(directory.Listings)
	if err != nil {
		return PushPlan{}, err
	}

	locales := make([]string, 0, len(localListings))
	for locale := range localListings {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	updatedListings := make(map[string]json.RawMessage, len(locales))
	changes := make([]ListingChange, 0)
	for _, locale := range locales {
		local := localListings[locale]
		rawListing, exists := rawListings[locale]
		if !exists {
			changes = append(changes, ListingChange{Locale: locale, Action: "add"})
			rawListing = json.RawMessage(`{}`)
		} else {
			changes = append(changes, diffListing(locale, local, serverSnapshot.Listings[locale])...)
		}
		updated, updateErr := applyListing(rawListing, local, imageDiff.Images[locale])
		if updateErr != nil {
			return PushPlan{}, updateErr
		}
		key := originalKeys[locale]
		if key == "" {
			key = locale
		}
		updatedListings[key] = updated
	}
	for locale := range serverSnapshot.Listings {
		if _, exists := localListings[locale]; !exists {
			changes = append(changes, ListingChange{Locale: locale, Action: "remove"})
		}
	}
	sort.Slice(changes, func(left, right int) bool {
		leftKey := changes[left].Locale + "\x00" + changes[left].Action + "\x00" + changes[left].Field
		rightKey := changes[right].Locale + "\x00" + changes[right].Action + "\x00" + changes[right].Field
		return leftKey < rightKey
	})

	body := make(map[string]json.RawMessage, len(listingPushAllowlist)+3)
	for _, property := range listingPushAllowlist {
		if value, exists := source[property]; exists {
			body[property] = cloneRaw(value)
		}
	}
	encodedListings, err := json.Marshal(updatedListings)
	if err != nil {
		return PushPlan{}, fmt.Errorf("encode submission listings: %w", err)
	}
	body["listings"] = encodedListings
	body["targetPublishMode"] = json.RawMessage(`"Immediate"`)
	for _, property := range []string{"applicationPackages", "packageDeliveryOptions"} {
		if value, exists := source[property]; exists {
			body[property] = cloneRaw(value)
		}
	}
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return PushPlan{}, fmt.Errorf("encode listing push body: %w", err)
	}
	return PushPlan{
		Body: encodedBody, ListingChanges: changes, ImageChanges: imageDiff.Changes, Uploads: imageDiff.Uploads,
	}, nil
}

func decodeRawListings(raw json.RawMessage) (map[string]json.RawMessage, map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil, errors.New("store submission does not contain any listings")
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, nil, fmt.Errorf("decode submission listings: %w", err)
	}
	result := make(map[string]json.RawMessage, len(source))
	original := make(map[string]string, len(source))
	for rawLocale, listing := range source {
		locale, err := canonicalLocale(rawLocale)
		if err != nil {
			return nil, nil, err
		}
		if _, duplicate := result[locale]; duplicate {
			return nil, nil, fmt.Errorf("submission listing locale %q is duplicated case-insensitively", rawLocale)
		}
		result[locale] = listing
		original[locale] = rawLocale
	}
	return result, original, nil
}

func canonicalListings(source map[string]Listing) (map[string]Listing, error) {
	result := make(map[string]Listing, len(source))
	for rawLocale, listing := range source {
		locale, err := canonicalLocale(rawLocale)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[locale]; duplicate {
			return nil, fmt.Errorf("listing locale %q is duplicated case-insensitively", rawLocale)
		}
		result[locale] = listing
	}
	return result, nil
}

func diffListing(locale string, local, server Listing) []ListingChange {
	changes := make([]ListingChange, 0)
	fields := []struct {
		name  string
		equal bool
	}{
		{"title", local.Title == server.Title},
		{"description", local.Description == server.Description},
		{"shortDescription", local.ShortDescription == server.ShortDescription},
		{"features", reflect.DeepEqual(local.Features, server.Features)},
		{"keywords", reflect.DeepEqual(local.Keywords, server.Keywords)},
		{"copyrightAndTrademarkInfo", local.CopyrightAndTrademarkInfo == server.CopyrightAndTrademarkInfo},
		{"licenseTerms", local.LicenseTerms == server.LicenseTerms},
		{"recommendedHardware", rawJSONEqual(local.RecommendedHardware, server.RecommendedHardware)},
		{"minimumHardware", rawJSONEqual(local.MinimumHardware, server.MinimumHardware)},
	}
	for _, field := range fields {
		if !field.equal {
			changes = append(changes, ListingChange{Locale: locale, Action: "update", Field: field.name})
		}
	}
	return changes
}

func rawJSONEqual(left, right json.RawMessage) bool {
	if len(left) == 0 {
		left = json.RawMessage(`null`)
	}
	if len(right) == 0 {
		right = json.RawMessage(`null`)
	}
	return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right)) || jsonSemanticEqual(left, right)
}

func jsonSemanticEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func applyListing(rawListing json.RawMessage, local Listing, images []json.RawMessage) (json.RawMessage, error) {
	listing := map[string]json.RawMessage{}
	if len(rawListing) > 0 && string(rawListing) != "null" {
		if err := json.Unmarshal(rawListing, &listing); err != nil {
			return nil, fmt.Errorf("decode store listing: %w", err)
		}
	}
	base := map[string]json.RawMessage{}
	if rawBase, exists := listing["baseListing"]; exists && string(rawBase) != "null" {
		if err := json.Unmarshal(rawBase, &base); err != nil {
			return nil, fmt.Errorf("decode store baseListing: %w", err)
		}
	}
	setJSONField(base, "title", local.Title)
	setJSONField(base, "description", local.Description)
	setJSONField(base, "shortDescription", local.ShortDescription)
	setJSONField(base, "features", local.Features)
	setJSONField(base, "keywords", local.Keywords)
	setJSONField(base, "copyrightAndTrademarkInfo", local.CopyrightAndTrademarkInfo)
	setJSONField(base, "licenseTerms", local.LicenseTerms)
	base["recommendedHardware"] = rawOrNull(local.RecommendedHardware)
	base["minimumHardware"] = rawOrNull(local.MinimumHardware)
	encodedImages, err := json.Marshal(images)
	if err != nil {
		return nil, fmt.Errorf("encode listing images: %w", err)
	}
	base["images"] = encodedImages
	encodedBase, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("encode store baseListing: %w", err)
	}
	listing["baseListing"] = encodedBase
	encoded, err := json.Marshal(listing)
	if err != nil {
		return nil, fmt.Errorf("encode store listing: %w", err)
	}
	return encoded, nil
}

func setJSONField(target map[string]json.RawMessage, field string, value any) {
	encoded, _ := json.Marshal(value)
	target[field] = encoded
}

func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`null`)
	}
	return cloneRaw(raw)
}
