package submission

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type localizedNote struct {
	originalLocale string
	value          string
}

// ApplyReleaseNotes applies the notes contract to every Store listing while preserving unknown fields.
func ApplyReleaseNotes(submissionJSON json.RawMessage, notesJSON []byte, sourceName string) (json.RawMessage, []string, error) {
	var submissionObject map[string]json.RawMessage
	if err := json.Unmarshal(submissionJSON, &submissionObject); err != nil {
		return nil, nil, fmt.Errorf("decode submission: %w", err)
	}
	var listings map[string]json.RawMessage
	if rawListings, ok := submissionObject["listings"]; !ok || string(rawListings) == "null" {
		return nil, nil, errors.New("store submission does not contain any listings; cannot apply release notes")
	} else if err := json.Unmarshal(rawListings, &listings); err != nil {
		return nil, nil, fmt.Errorf("decode submission listings: %w", err)
	}
	if len(listings) == 0 {
		return nil, nil, errors.New("store submission does not contain any listings; cannot apply release notes")
	}
	notes, err := parseReleaseNotes(notesJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("release notes file %q: %w", sourceName, err)
	}

	missing := make([]string, 0)
	for locale := range listings {
		if _, ok := notes[strings.ToLower(locale)]; !ok {
			missing = append(missing, locale)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, nil, fmt.Errorf("release notes file %q is missing notes for Store listing locale(s): %s", sourceName, strings.Join(missing, ", "))
	}

	listingLocaleKeys := make(map[string]struct{}, len(listings))
	for locale, rawListing := range listings {
		localeKey := strings.ToLower(locale)
		listingLocaleKeys[localeKey] = struct{}{}
		var listing map[string]json.RawMessage
		if err := json.Unmarshal(rawListing, &listing); err != nil {
			return nil, nil, fmt.Errorf("decode Store listing %q: %w", locale, err)
		}
		rawBase, ok := listing["baseListing"]
		if !ok || string(rawBase) == "null" {
			return nil, nil, fmt.Errorf("store listing %q does not contain a baseListing; cannot apply release notes", locale)
		}
		var base map[string]json.RawMessage
		if err := json.Unmarshal(rawBase, &base); err != nil {
			return nil, nil, fmt.Errorf("decode Store listing %q baseListing: %w", locale, err)
		}
		encodedNote, _ := json.Marshal(notes[localeKey].value)
		base["releaseNotes"] = encodedNote
		encodedBase, err := json.Marshal(base)
		if err != nil {
			return nil, nil, fmt.Errorf("encode Store listing %q baseListing: %w", locale, err)
		}
		listing["baseListing"] = encodedBase
		encodedListing, err := json.Marshal(listing)
		if err != nil {
			return nil, nil, fmt.Errorf("encode Store listing %q: %w", locale, err)
		}
		listings[locale] = encodedListing
	}

	warnings := make([]string, 0)
	for localeKey, note := range notes {
		if _, ok := listingLocaleKeys[localeKey]; !ok {
			warnings = append(warnings, fmt.Sprintf("release notes file %q contains unused locale %q", sourceName, note.originalLocale))
		}
	}
	sort.Strings(warnings)
	encodedListings, err := json.Marshal(listings)
	if err != nil {
		return nil, nil, fmt.Errorf("encode submission listings: %w", err)
	}
	submissionObject["listings"] = encodedListings
	updated, err := json.Marshal(submissionObject)
	if err != nil {
		return nil, nil, fmt.Errorf("encode submission: %w", err)
	}
	return updated, warnings, nil
}

func parseReleaseNotes(data []byte) (map[string]localizedNote, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	rawNotes, ok := root["notes"]
	if !ok {
		return nil, errors.New("top-level notes object is required")
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(rawNotes, &source); err != nil {
		return nil, errors.New("top-level notes must be an object")
	}
	result := make(map[string]localizedNote, len(source))
	for locale, rawValue := range source {
		localeKey := strings.ToLower(locale)
		if existing, duplicate := result[localeKey]; duplicate {
			return nil, fmt.Errorf("locale %q duplicates %q case-insensitively", locale, existing.originalLocale)
		}
		value, err := parseReleaseNoteValue(locale, rawValue)
		if err != nil {
			return nil, err
		}
		result[localeKey] = localizedNote{originalLocale: locale, value: value}
	}
	return result, nil
}

func parseReleaseNoteValue(locale string, raw json.RawMessage) (string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		single = strings.TrimSpace(single)
		if single == "" {
			return "", fmt.Errorf("release notes for %q are empty", locale)
		}
		return single, nil
	}
	var rawLines []json.RawMessage
	if err := json.Unmarshal(raw, &rawLines); err != nil {
		return "", fmt.Errorf("release notes for %q must be a string or an array of strings", locale)
	}
	lines := make([]string, 0, len(rawLines))
	for _, rawLine := range rawLines {
		if string(rawLine) == "null" {
			continue
		}
		var line string
		if err := json.Unmarshal(rawLine, &line); err != nil {
			return "", fmt.Errorf("release notes for %q must be an array of strings", locale)
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	value := strings.Join(lines, "\r\n")
	if value == "" {
		return "", fmt.Errorf("release notes for %q are empty", locale)
	}
	return value, nil
}
