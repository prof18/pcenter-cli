package output

import "errors"

// Error codes are the stable contract automation branches on. The human message
// may be reworded freely; a code may not change meaning once released.
const (
	// CodeUsage is invalid CLI usage — wrong flags, bad argument values.
	CodeUsage = "usage"
	// CodeMissingConfiguration is credentials that could not be resolved.
	CodeMissingConfiguration = "missing_configuration"
	// CodeEnvFile is a credentials file that is absent or unreadable when one was named explicitly.
	CodeEnvFile = "env_file"
	// CodeAuthFailed is credentials the Store rejected.
	CodeAuthFailed = "auth_failed"
	// CodeStateConflict is an operation invalid for the resource's current state (HTTP 409).
	// It is permanent: retrying without changing the state cannot succeed.
	CodeStateConflict = "state_conflict"
	// CodeNotFound is a missing app, submission, or rollout (HTTP 404).
	CodeNotFound = "not_found"
	// CodeRateLimited is throttling that outlasted the retry budget (HTTP 429).
	CodeRateLimited = "rate_limited"
	// CodeAPIError is any other unsuccessful Store response.
	CodeAPIError = "api_error"
	// CodeValidation is input rejected before any request was sent.
	CodeValidation = "validation"
	// CodeFailure is an otherwise unclassified runtime failure.
	CodeFailure = "failure"
)

// Coded is implemented by errors carrying a machine-readable code.
type Coded interface {
	ErrorCode() string
}

// Detailed is implemented by errors contributing structured fields to JSON
// error output, so an agent does not have to parse the message to act.
type Detailed interface {
	ErrorDetails() map[string]any
}

// ExitCodeFor maps an error code to a process exit code. Automation can branch
// on $? alone for the distinctions that change what to do next, without reading
// stdout at all.
func ExitCodeFor(code string) int {
	switch code {
	case CodeUsage, CodeMissingConfiguration, CodeEnvFile, CodeValidation:
		return ExitUsage
	case CodeAuthFailed:
		return ExitAuth
	case CodeStateConflict:
		return ExitStateConflict
	case CodeRateLimited:
		return ExitRateLimited
	default:
		return ExitFailure
	}
}

// CodeOf reports the code of the first error in the chain that carries one.
func CodeOf(err error) string {
	var coded Coded
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return ""
}

// DetailsOf merges structured fields from every error in the chain that has
// them, outermost last so a wrapper can override what it wraps.
func DetailsOf(err error) map[string]any {
	var chain []Detailed
	for current := err; current != nil; current = errors.Unwrap(current) {
		if detailed, ok := current.(Detailed); ok {
			chain = append(chain, detailed)
		}
	}
	if len(chain) == 0 {
		return nil
	}
	merged := map[string]any{}
	for index := len(chain) - 1; index >= 0; index-- {
		for key, value := range chain[index].ErrorDetails() {
			merged[key] = value
		}
	}
	return merged
}
