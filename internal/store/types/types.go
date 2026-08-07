// Package types contains Partner Center JSON models used by the CLI.
package types

import "encoding/json"

// SubmissionReference is embedded in an application response.
//
// It carries no status: the application resource returns only an id and a
// resource location for each submission, so a status has to come from the
// submission itself. Verified against the live API on 2026-08-06.
type SubmissionReference struct {
	ID               string `json:"id"`
	ResourceLocation string `json:"resourceLocation,omitempty"`
}

// Application is the app resource returned by Partner Center.
//
// The display name is `primaryName`, not `name` — modelling it as `name` meant
// every command printed a blank app name. `PackageFamilyName` really is
// capitalised that way in the response; Go's decoder matches case-insensitively
// so the tag works either way, but it is not a typo.
type Application struct {
	ID                                 string               `json:"id"`
	PrimaryName                        string               `json:"primaryName,omitempty"`
	PackageFamilyName                  string               `json:"packageFamilyName,omitempty"`
	PackageIdentityName                string               `json:"packageIdentityName,omitempty"`
	PublisherName                      string               `json:"publisherName,omitempty"`
	FirstPublishedDate                 string               `json:"firstPublishedDate,omitempty"`
	HasAdvancedListingPermission       bool                 `json:"hasAdvancedListingPermission,omitempty"`
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
