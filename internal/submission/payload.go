package submission

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var publishAllowlist = []string{
	"applicationCategory", "pricing", "visibility", "targetPublishDate", "listings",
	"hardwarePreferences", "automaticBackupEnabled", "canInstallOnRemovableMedia",
	"isGameDvrEnabled", "gamingOptions", "hasExternalInAppProducts",
	"meetAccessibilityGuidelines", "notesForCertification", "enterpriseLicensing",
	"allowMicrosoftDecideAppAvailabilityToFutureDeviceFamilies",
	"allowTargetFutureDeviceFamilies", "trailers",
}

// BuildPublishBody constructs the allowlisted full-document PUT body for an MSIX release.
func BuildPublishBody(submissionJSON json.RawMessage, packageFileName string, rolloutPercentage float64) (json.RawMessage, error) {
	if strings.TrimSpace(packageFileName) == "" {
		return nil, errors.New("package file name is required")
	}
	if rolloutPercentage <= 0 || rolloutPercentage > 100 {
		return nil, errors.New("rollout percentage must be greater than 0 and at most 100")
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(submissionJSON, &source); err != nil {
		return nil, fmt.Errorf("decode submission JSON: %w", err)
	}
	body := make(map[string]json.RawMessage, len(publishAllowlist)+3)
	for _, property := range publishAllowlist {
		if value, ok := source[property]; ok {
			body[property] = append(json.RawMessage(nil), value...)
		}
	}
	body["targetPublishMode"] = json.RawMessage(`"Immediate"`)

	packages, err := buildPackages(source["applicationPackages"], packageFileName)
	if err != nil {
		return nil, err
	}
	body["applicationPackages"] = packages
	delivery, err := buildDeliveryOptions(source["packageDeliveryOptions"], rolloutPercentage)
	if err != nil {
		return nil, err
	}
	body["packageDeliveryOptions"] = delivery

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode publish body: %w", err)
	}
	return encoded, nil
}

type versionedPackage struct {
	fields  map[string]json.RawMessage
	version [4]uint64
}

func buildPackages(raw json.RawMessage, newFileName string) (json.RawMessage, error) {
	var source []map[string]json.RawMessage
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &source); err != nil {
			return nil, fmt.Errorf("decode applicationPackages: %w", err)
		}
	}
	packages := make([]versionedPackage, 0, len(source))
	for index, fields := range source {
		var versionText string
		if err := json.Unmarshal(fields["version"], &versionText); err != nil {
			return nil, fmt.Errorf("applicationPackages[%d] has invalid version: %w", index, err)
		}
		version, err := parseFourPartVersion(versionText)
		if err != nil {
			return nil, fmt.Errorf("applicationPackages[%d]: %w", index, err)
		}
		packages = append(packages, versionedPackage{fields: cloneRawObject(fields), version: version})
	}
	sort.SliceStable(packages, func(left, right int) bool {
		for index := range packages[left].version {
			if packages[left].version[index] != packages[right].version[index] {
				return packages[left].version[index] > packages[right].version[index]
			}
		}
		return false
	})
	result := make([]map[string]json.RawMessage, 0, len(packages)+1)
	for index, existing := range packages {
		if index > 0 {
			existing.fields["fileStatus"] = json.RawMessage(`"PendingDelete"`)
		}
		result = append(result, existing.fields)
	}
	fileName, _ := json.Marshal(newFileName)
	result = append(result, map[string]json.RawMessage{
		"fileName":              fileName,
		"fileStatus":            json.RawMessage(`"PendingUpload"`),
		"minimumDirectXVersion": json.RawMessage(`"None"`),
		"minimumSystemRam":      json.RawMessage(`"None"`),
	})
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode applicationPackages: %w", err)
	}
	return encoded, nil
}

func buildDeliveryOptions(raw json.RawMessage, percentage float64) (json.RawMessage, error) {
	delivery := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &delivery); err != nil {
			return nil, fmt.Errorf("decode packageDeliveryOptions: %w", err)
		}
	} else {
		delivery["isMandatoryUpdate"] = json.RawMessage(`false`)
		delivery["mandatoryUpdateEffectiveDate"] = json.RawMessage(`"1601-01-01T00:00:00.0000000Z"`)
	}
	rollout := map[string]json.RawMessage{}
	if rawRollout, ok := delivery["packageRollout"]; ok && string(rawRollout) != "null" {
		if err := json.Unmarshal(rawRollout, &rollout); err != nil {
			return nil, fmt.Errorf("decode packageDeliveryOptions.packageRollout: %w", err)
		}
	}
	rollout["isPackageRollout"] = json.RawMessage(`true`)
	encodedPercentage, _ := json.Marshal(percentage)
	rollout["packageRolloutPercentage"] = encodedPercentage
	encodedRollout, err := json.Marshal(rollout)
	if err != nil {
		return nil, fmt.Errorf("encode package rollout: %w", err)
	}
	delivery["packageRollout"] = encodedRollout
	encoded, err := json.Marshal(delivery)
	if err != nil {
		return nil, fmt.Errorf("encode packageDeliveryOptions: %w", err)
	}
	return encoded, nil
}

func parseFourPartVersion(value string) ([4]uint64, error) {
	var result [4]uint64
	parts := strings.Split(value, ".")
	if len(parts) != len(result) {
		return result, fmt.Errorf("version %q must contain four numeric parts", value)
	}
	for index, part := range parts {
		parsed, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, fmt.Errorf("version %q contains a non-numeric part", value)
		}
		result[index] = parsed
	}
	return result, nil
}

func cloneRawObject(source map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(source))
	for key, value := range source {
		result[key] = append(json.RawMessage(nil), value...)
	}
	return result
}
