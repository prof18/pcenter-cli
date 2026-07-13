// Package types contains Partner Center JSON models used by the CLI.
package types

import "encoding/json"

// SubmissionReference is embedded in an application response.
type SubmissionReference struct {
	ID            string          `json:"id"`
	Status        string          `json:"status,omitempty"`
	StatusDetails json.RawMessage `json:"statusDetails,omitempty"`
}

// Application is the app resource returned by Partner Center.
type Application struct {
	ID                                 string               `json:"id"`
	Name                               string               `json:"name,omitempty"`
	LastPublishedApplicationSubmission *SubmissionReference `json:"lastPublishedApplicationSubmission,omitempty"`
	PendingApplicationSubmission       *SubmissionReference `json:"pendingApplicationSubmission,omitempty"`
}

// Submission is the read-only subset needed in M1. Raw preserves the complete response.
type Submission struct {
	ID            string                     `json:"id"`
	Status        string                     `json:"status,omitempty"`
	StatusDetails json.RawMessage            `json:"statusDetails,omitempty"`
	FileUploadURL string                     `json:"fileUploadUrl,omitempty"`
	Listings      map[string]json.RawMessage `json:"listings,omitempty"`
	Raw           json.RawMessage            `json:"-"`
}

// SubmissionStatus is returned by the dedicated status endpoint.
type SubmissionStatus struct {
	Status        string          `json:"status"`
	StatusDetails json.RawMessage `json:"statusDetails,omitempty"`
}

// Rollout is the package rollout state for a published submission.
type Rollout struct {
	IsPackageRollout         bool    `json:"isPackageRollout"`
	PackageRolloutPercentage float64 `json:"packageRolloutPercentage"`
	PackageRolloutStatus     string  `json:"packageRolloutStatus"`
	FallbackSubmissionID     string  `json:"fallbackSubmissionId,omitempty"`
}

// ReviewPage is a page from the analytics reviews API.
type ReviewPage struct {
	Value      []json.RawMessage `json:"Value"`
	TotalCount int               `json:"TotalCount"`
	NextLink   string            `json:"@nextLink,omitempty"`
}

// Review is the display subset of a review; raw JSON remains available through ReviewPage.Value.
type Review struct {
	ID             string `json:"id"`
	Date           string `json:"date"`
	Market         string `json:"market"`
	Rating         int    `json:"rating"`
	ReviewTitle    string `json:"reviewTitle"`
	ReviewText     string `json:"reviewText"`
	PackageVersion string `json:"packageVersion"`
}
