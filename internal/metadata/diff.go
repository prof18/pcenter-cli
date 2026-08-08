package metadata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StoreImage is the typed image subset used for matching. Raw preserves unknown Store fields.
type StoreImage struct {
	FileName    string          `json:"fileName"`
	FileStatus  string          `json:"fileStatus"`
	ID          string          `json:"id,omitempty"`
	Description string          `json:"description,omitempty"`
	ImageType   string          `json:"imageType"`
	Raw         json.RawMessage `json:"-"`
}

// ImageUpload is a local file destined for the Store upload ZIP.
type ImageUpload struct {
	SourcePath string
	Name       string
}

// ImageChange is a stable, user-facing image diff item.
type ImageChange struct {
	Locale    string `json:"locale"`
	Action    string `json:"action"`
	LocalPath string `json:"localPath,omitempty"`
	StoreID   string `json:"storeId,omitempty"`
}

// ImageDiff contains the per-locale PUT payload, upload inputs, and display changes.
type ImageDiff struct {
	Images  map[string][]json.RawMessage
	Uploads []ImageUpload
	Changes []ImageChange
}

// DiffImages computes an explicit, non-destructive image patch.
func DiffImages(metadataDir string, manifest ImageManifest, server map[string][]StoreImage) (ImageDiff, error) {
	if err := ValidateImages(metadataDir, manifest); err != nil {
		return ImageDiff{}, err
	}
	serverByLocale, err := canonicalServerImages(server)
	if err != nil {
		return ImageDiff{}, err
	}
	manifestByLocale, err := canonicalManifestImages(manifest.Images)
	if err != nil {
		return ImageDiff{}, err
	}
	// Changes and Uploads are only ever appended to, and appending nothing to a
	// nil slice leaves it nil — which serializes as JSON null while the sibling
	// listingChanges renders as []. A caller taking the length of one field and
	// not the other is a bug we would be handing out, so both start empty.
	result := ImageDiff{
		Images:  make(map[string][]json.RawMessage),
		Uploads: make([]ImageUpload, 0),
		Changes: make([]ImageChange, 0),
	}
	localeSet := make(map[string]struct{}, len(serverByLocale)+len(manifestByLocale))
	for locale := range serverByLocale {
		localeSet[locale] = struct{}{}
	}
	for locale := range manifestByLocale {
		localeSet[locale] = struct{}{}
	}
	locales := make([]string, 0, len(localeSet))
	for locale := range localeSet {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	for _, locale := range locales {
		updates, uploads, changes, diffErr := diffLocaleImages(metadataDir, locale, manifestByLocale[locale], serverByLocale[locale])
		if diffErr != nil {
			return ImageDiff{}, diffErr
		}
		result.Images[locale] = updates
		result.Uploads = append(result.Uploads, uploads...)
		result.Changes = append(result.Changes, changes...)
	}
	sort.Slice(result.Uploads, func(left, right int) bool { return result.Uploads[left].Name < result.Uploads[right].Name })
	sort.Slice(result.Changes, func(left, right int) bool {
		leftKey := result.Changes[left].Locale + "\x00" + result.Changes[left].Action + "\x00" + result.Changes[left].LocalPath + "\x00" + result.Changes[left].StoreID
		rightKey := result.Changes[right].Locale + "\x00" + result.Changes[right].Action + "\x00" + result.Changes[right].LocalPath + "\x00" + result.Changes[right].StoreID
		return leftKey < rightKey
	})
	return result, nil
}

func diffLocaleImages(metadataDir, locale string, manifest []ImageEntry, server []StoreImage) ([]json.RawMessage, []ImageUpload, []ImageChange, error) {
	updates := make([]json.RawMessage, len(server), len(server)+len(manifest))
	byID := make(map[string]int, len(server))
	byFileName := make(map[string]int, len(server))
	for index, image := range server {
		raw, err := rawStoreImage(image)
		if err != nil {
			return nil, nil, nil, err
		}
		updates[index] = raw
		if image.ID != "" {
			if _, duplicate := byID[image.ID]; duplicate {
				return nil, nil, nil, fmt.Errorf("store locale %q contains duplicate image id %q", locale, image.ID)
			}
			byID[image.ID] = index
		}
		if image.FileName != "" {
			byFileName[image.FileName] = index
		}
	}
	usedStoreIDs := make(map[string]struct{})
	pendingUploads := make([]json.RawMessage, 0)
	uploads := make([]ImageUpload, 0)
	changes := make([]ImageChange, 0)
	for _, entry := range manifest {
		if entry.StoreID != "" {
			if _, duplicate := usedStoreIDs[entry.StoreID]; duplicate {
				return nil, nil, nil, fmt.Errorf("image manifest locale %q references Store image %q more than once", locale, entry.StoreID)
			}
			usedStoreIDs[entry.StoreID] = struct{}{}
		}
		if entry.RemoteOnly || entry.Delete || entry.StoreID != "" {
			index, exists := byID[entry.StoreID]
			if !exists {
				return nil, nil, nil, fmt.Errorf("image manifest locale %q references missing Store image %q", locale, entry.StoreID)
			}
			if entry.RemoteOnly {
				continue
			}
			if entry.Delete {
				updated, updateErr := mutateImageRaw(updates[index], map[string]any{"fileStatus": "PendingDelete"})
				if updateErr != nil {
					return nil, nil, nil, updateErr
				}
				updates[index] = updated
				changes = append(changes, ImageChange{Locale: locale, Action: "delete", StoreID: entry.StoreID})
				continue
			}
			actualHash, hashErr := imageSHA256(metadataDir, entry.LocalPath)
			if hashErr != nil {
				return nil, nil, nil, hashErr
			}
			serverImage := server[index]
			if entry.SHA256 != "" && strings.EqualFold(entry.SHA256, actualHash) && serverImage.FileName == entry.LocalPath {
				fields := map[string]any{"fileStatus": "Uploaded"}
				if serverImage.Description != entry.Description {
					fields["description"] = entry.Description
					changes = append(changes, ImageChange{Locale: locale, Action: "caption", LocalPath: entry.LocalPath, StoreID: entry.StoreID})
				}
				updated, updateErr := mutateImageRaw(updates[index], fields)
				if updateErr != nil {
					return nil, nil, nil, updateErr
				}
				updates[index] = updated
				continue
			}
			deleted, updateErr := mutateImageRaw(updates[index], map[string]any{"fileStatus": "PendingDelete"})
			if updateErr != nil {
				return nil, nil, nil, updateErr
			}
			updates[index] = deleted
			pending, pendingErr := pendingUploadImage(entry)
			if pendingErr != nil {
				return nil, nil, nil, pendingErr
			}
			pendingUploads = append(pendingUploads, pending)
			uploads = append(uploads, imageUpload(metadataDir, entry.LocalPath))
			changes = append(changes, ImageChange{Locale: locale, Action: "replace", LocalPath: entry.LocalPath, StoreID: entry.StoreID})
			continue
		}
		if _, collision := byFileName[entry.LocalPath]; collision {
			return nil, nil, nil, fmt.Errorf("new image %q in locale %q collides with an existing Store fileName", entry.LocalPath, locale)
		}
		pending, pendingErr := pendingUploadImage(entry)
		if pendingErr != nil {
			return nil, nil, nil, pendingErr
		}
		pendingUploads = append(pendingUploads, pending)
		uploads = append(uploads, imageUpload(metadataDir, entry.LocalPath))
		changes = append(changes, ImageChange{Locale: locale, Action: "add", LocalPath: entry.LocalPath})
	}
	sort.Slice(pendingUploads, func(left, right int) bool {
		return imageFileName(pendingUploads[left]) < imageFileName(pendingUploads[right])
	})
	updates = append(updates, pendingUploads...)
	sort.SliceStable(updates, func(left, right int) bool {
		return imageFileName(updates[left]) < imageFileName(updates[right])
	})
	return updates, uploads, changes, nil
}

func canonicalServerImages(source map[string][]StoreImage) (map[string][]StoreImage, error) {
	result := make(map[string][]StoreImage, len(source))
	for rawLocale, images := range source {
		locale, err := canonicalLocale(rawLocale)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[locale]; duplicate {
			return nil, fmt.Errorf("store image locale %q is duplicated case-insensitively", rawLocale)
		}
		result[locale] = images
	}
	return result, nil
}

func canonicalManifestImages(source map[string][]ImageEntry) (map[string][]ImageEntry, error) {
	result := make(map[string][]ImageEntry, len(source))
	for rawLocale, images := range source {
		locale, err := canonicalLocale(rawLocale)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[locale]; duplicate {
			return nil, fmt.Errorf("image manifest locale %q is duplicated case-insensitively", rawLocale)
		}
		result[locale] = images
	}
	return result, nil
}

func rawStoreImage(image StoreImage) (json.RawMessage, error) {
	if len(image.Raw) > 0 {
		return append(json.RawMessage(nil), image.Raw...), nil
	}
	raw, err := json.Marshal(image)
	if err != nil {
		return nil, fmt.Errorf("encode Store image %q: %w", image.ID, err)
	}
	return raw, nil
}

func mutateImageRaw(raw json.RawMessage, fields map[string]any) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("decode Store image: %w", err)
	}
	for key, value := range fields {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode Store image field %s: %w", key, err)
		}
		object[key] = encoded
	}
	updated, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode Store image: %w", err)
	}
	return updated, nil
}

func pendingUploadImage(entry ImageEntry) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"fileName": entry.LocalPath, "fileStatus": "PendingUpload",
		"description": entry.Description, "imageType": entry.ImageType,
	})
}

func imageSHA256(metadataDir, localPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(metadataDir, "images", filepath.FromSlash(localPath)))
	if err != nil {
		return "", fmt.Errorf("hash local image %q: %w", localPath, err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func imageUpload(metadataDir, localPath string) ImageUpload {
	return ImageUpload{SourcePath: filepath.Join(metadataDir, "images", filepath.FromSlash(localPath)), Name: localPath}
}

func imageFileName(raw json.RawMessage) string {
	var image struct {
		FileName string `json:"fileName"`
	}
	_ = json.Unmarshal(raw, &image)
	return image.FileName
}
