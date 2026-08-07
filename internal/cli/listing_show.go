package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	metadataflow "github.com/prof18/pcenter-cli/internal/metadata"
	"github.com/prof18/pcenter-cli/internal/output"
	storetypes "github.com/prof18/pcenter-cli/internal/store/types"
)

// listingShowResult is the whole listing as data. `listing pull` writes the
// same content to disk; this exists because reading the current text should not
// require creating a directory, and digging it out of a full `submission get`
// means parsing the entire submission.
type listingShowResult struct {
	Source       string                  `json:"source"`
	SubmissionID string                  `json:"submissionId"`
	LocaleCount  int                     `json:"localeCount"`
	Listings     map[string]listingEntry `json:"listings"`
}

type listingEntry struct {
	metadataflow.Listing
	// ImageCount is how many images the Store holds for this locale. The
	// binaries are not downloadable through the API, so only the count and the
	// captions are available here.
	ImageCount int            `json:"imageCount"`
	Images     []listingImage `json:"images,omitempty"`
}

type listingImage struct {
	ImageType   string `json:"imageType"`
	Description string `json:"description,omitempty"`
	StoreID     string `json:"storeId,omitempty"`
}

func (s *commandState) listingShowCommand() *cobra.Command {
	var locale string
	var published, pending, withImages bool

	command := &cobra.Command{
		Use:   "show",
		Short: "Print the Store listing without writing anything to disk",
		Long: strings.TrimSpace(`
Print the current Store listing.

Reads the same content "listing pull" writes, but to stdout, so inspecting a
listing needs no directory and no cleanup. Use --locale to limit output to one
locale; without it every locale is returned.

Image binaries cannot be downloaded through the Submission API, so --images
reports each image's type, caption and Store id rather than the file itself.`),
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if published && pending {
				return usageError{errors.New("pass at most one of --published or --pending")}
			}
			if err := s.prepareOutput(); err != nil {
				return err
			}
			if err := s.prepareClient(); err != nil {
				return err
			}

			submission, source, err := s.resolveListingSource(cmd.Context(), pending)
			if err != nil {
				return err
			}
			build := normalizeBuildInfo(s.dependencies.Build)
			// A throwaway directory name: nothing is written, but the snapshot
			// builder is the one place that knows the listing contract, and
			// reimplementing it here would let the two drift.
			snapshot, serverImages, err := metadataflow.SnapshotFromSubmission(
				".", s.config.AppID, "pcenter "+build.Version, s.dependencies.Now(), submission.Raw)
			if err != nil {
				return failureError{err}
			}

			result := listingShowResult{
				Source:       source,
				SubmissionID: snapshot.Marker.SourceSubmissionID,
				Listings:     make(map[string]listingEntry, len(snapshot.Listings)),
			}
			wanted := strings.ToLower(strings.TrimSpace(locale))
			for name, listing := range snapshot.Listings {
				if wanted != "" && !strings.EqualFold(name, wanted) {
					continue
				}
				entry := listingEntry{Listing: listing, ImageCount: len(serverImages[name])}
				if withImages {
					for _, image := range serverImages[name] {
						entry.Images = append(entry.Images, listingImage{
							ImageType: image.ImageType, Description: image.Description, StoreID: image.ID,
						})
					}
				}
				result.Listings[name] = entry
			}
			if wanted != "" && len(result.Listings) == 0 {
				return failureError{fmt.Errorf("listing has no locale %q; run \"pcenter locales list\" to see the %d available", locale, len(snapshot.Listings))}
			}
			result.LocaleCount = len(result.Listings)
			return s.renderListingShow(result, wanted != "")
		},
	}

	command.Flags().StringVar(&locale, "locale", "", "limit output to one locale")
	command.Flags().BoolVar(&published, "published", false, "read the last published submission (default)")
	command.Flags().BoolVar(&pending, "pending", false, "read the pending submission")
	command.Flags().BoolVar(&withImages, "images", false, "include image types, captions and Store ids")
	return command
}

// resolveListingSource picks the published or pending submission and fetches it.
func (s *commandState) resolveListingSource(ctx context.Context, pending bool) (storetypes.Submission, string, error) {
	app, err := s.client.Application(ctx, s.config.AppID)
	if err != nil {
		return storetypes.Submission{}, "", failureError{err}
	}
	reference := app.LastPublishedApplicationSubmission
	source := "published"
	if pending {
		reference = app.PendingApplicationSubmission
		source = "pending"
	}
	if reference == nil {
		return storetypes.Submission{}, "", failureError{fmt.Errorf("application has no %s submission", source)}
	}
	submission, err := s.client.Submission(ctx, s.config.AppID, reference.ID)
	if err != nil {
		return storetypes.Submission{}, "", failureError{err}
	}
	return submission, source, nil
}

func (s *commandState) renderListingShow(result listingShowResult, singleLocale bool) error {
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	if s.format == output.JSON {
		return wrapFailure(renderer.Value(result))
	}

	locales := make([]string, 0, len(result.Listings))
	for name := range result.Listings {
		locales = append(locales, name)
	}
	sort.Strings(locales)

	// One locale is the case where a human wants to read the text, so print the
	// fields in full rather than truncating them into a row.
	if singleLocale && len(locales) == 1 {
		entry := result.Listings[locales[0]]
		rows := [][]string{
			{"locale", locales[0]},
			{"title", entry.Title},
			{"description", entry.Description},
			{"features", strings.Join(entry.Features, "\n")},
			{"keywords", strings.Join(entry.Keywords, ", ")},
			{"images", fmt.Sprintf("%d", entry.ImageCount)},
		}
		if entry.CopyrightAndTrademarkInfo != "" {
			rows = append(rows, []string{"copyright", entry.CopyrightAndTrademarkInfo})
		}
		if entry.LicenseTerms != "" {
			rows = append(rows, []string{"license terms", entry.LicenseTerms})
		}
		return wrapFailure(renderer.Rows([]string{"FIELD", "VALUE"}, rows))
	}

	rows := make([][]string, 0, len(locales))
	for _, name := range locales {
		entry := result.Listings[name]
		rows = append(rows, []string{
			name, entry.Title, fmt.Sprintf("%d", len(entry.Features)),
			fmt.Sprintf("%d", len(entry.Keywords)), fmt.Sprintf("%d", entry.ImageCount),
			fmt.Sprintf("%d", len(entry.Description)),
		})
	}
	return wrapFailure(renderer.Rows([]string{"LOCALE", "TITLE", "FEATURES", "KEYWORDS", "IMAGES", "DESC CHARS"}, rows))
}
