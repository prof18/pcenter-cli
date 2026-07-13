package store

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	bearerPattern       = regexp.MustCompile(`(?i)Bearer\s+[^\s,"']+`)
	clientSecretPattern = regexp.MustCompile(`(?i)(client_secret=)[^&\s,"']+`)
	urlQueryPattern     = regexp.MustCompile(`https?://[^\s"']+\?[^\s"']+`)
)

// Redactor centralizes credential removal for logs and error messages.
type Redactor struct {
	secrets []string
}

// NewRedactor creates a redactor for known secret values in addition to structural patterns.
func NewRedactor(secrets ...string) Redactor {
	filtered := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			filtered = append(filtered, secret)
		}
	}
	return Redactor{secrets: filtered}
}

// Redact removes known values, bearer credentials, client secrets, and URL queries.
func (r Redactor) Redact(value string) string {
	result := value
	for _, secret := range r.secrets {
		result = strings.ReplaceAll(result, secret, "[REDACTED]")
	}
	result = bearerPattern.ReplaceAllString(result, "Bearer [REDACTED]")
	result = clientSecretPattern.ReplaceAllString(result, "${1}[REDACTED]")
	result = urlQueryPattern.ReplaceAllStringFunc(result, redactURLQuery)
	return result
}

func redactURLQuery(value string) string {
	if index := strings.IndexByte(value, '?'); index >= 0 {
		return value[:index] + "?[REDACTED]"
	}
	return value
}

// RedactUploadURLsJSON removes query strings from all fileUploadUrl fields.
func RedactUploadURLsJSON(data []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	redactUploadURLValue(value)
	return json.Marshal(value)
}

func redactUploadURLValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.EqualFold(key, "fileUploadUrl") {
				if rawURL, ok := child.(string); ok {
					typed[key] = redactURLQuery(rawURL)
				}
				continue
			}
			redactUploadURLValue(child)
		}
	case []any:
		for _, child := range typed {
			redactUploadURLValue(child)
		}
	}
}
