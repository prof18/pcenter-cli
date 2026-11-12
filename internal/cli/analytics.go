package cli

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/prof18/pcenter-cli/internal/output"
	"github.com/prof18/pcenter-cli/internal/store"
	storetypes "github.com/prof18/pcenter-cli/internal/store/types"
)

// acquisitionGroupFields are the dimensions the acquisitions API can group
// by, keyed by lower case so the flag is case-insensitive. The value is the
// spelling the API expects. applicationName is accepted by the API but adds
// nothing for a single-app query, so it is not offered.
var acquisitionGroupFields = map[string]string{
	"date":            "date",
	"acquisitiontype": "acquisitionType",
	"agegroup":        "ageGroup",
	"storeclient":     "storeClient",
	"gender":          "gender",
	"market":          "market",
	"osversion":       "osVersion",
	"devicetype":      "deviceType",
	"ordername":       "orderName",
}

// acquisitionRow is one result row. Dimensions are omitted when the query did
// not group by them, so a row carries exactly the breakdown that was asked for.
type acquisitionRow struct {
	Date                string `json:"date"`
	AcquisitionQuantity int64  `json:"acquisitionQuantity"`
	OSVersion           string `json:"osVersion,omitempty"`
	Market              string `json:"market,omitempty"`
	DeviceType          string `json:"deviceType,omitempty"`
	AcquisitionType     string `json:"acquisitionType,omitempty"`
	StoreClient         string `json:"storeClient,omitempty"`
	AgeGroup            string `json:"ageGroup,omitempty"`
	Gender              string `json:"gender,omitempty"`
	OrderName           string `json:"orderName,omitempty"`
}

func (s *commandState) analyticsCommand() *cobra.Command {
	parent := &cobra.Command{Use: "analytics", Short: "Read Store analytics"}
	parent.AddCommand(s.analyticsAcquisitionsCommand())
	return parent
}

func (s *commandState) analyticsAcquisitionsCommand() *cobra.Command {
	var from, to, aggregation, groupBy, market, rawFilter string
	command := &cobra.Command{
		Use:   "acquisitions",
		Short: "List app acquisitions per day, week or month",
		Long: "List app acquisitions (downloads) aggregated per day, week or month.\n\n" +
			"Every page is fetched, so the rows add up to the full total for the range.\n" +
			"Partner Center analytics lag by a day or two, so the most recent days may be missing.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(from) == "" {
				return usageError{errors.New("analytics acquisitions requires --from")}
			}
			start, err := time.Parse("2006-01-02", from)
			if err != nil {
				return usageError{errors.New("--from: expected YYYY-MM-DD")}
			}
			end := s.dependencies.Now().UTC()
			if to != "" {
				end, err = time.Parse("2006-01-02", to)
				if err != nil {
					return usageError{errors.New("--to: expected YYYY-MM-DD")}
				}
			}
			if end.Format("2006-01-02") < start.Format("2006-01-02") {
				return usageError{errors.New("--to must not be before --from")}
			}
			switch aggregation {
			case "day", "week", "month":
			default:
				return usageError{errors.New("--aggregation must be day, week or month")}
			}
			fields, err := acquisitionGroupBy(groupBy)
			if err != nil {
				return usageError{fmt.Errorf("--group-by: %w", err)}
			}
			if err := s.prepareClient(); err != nil {
				return err
			}
			page, err := s.client.AppAcquisitions(cmd.Context(), store.AcquisitionQuery{
				ApplicationID: s.config.AppID, StartDate: start.Format("1/2/2006"), EndDate: end.Format("1/2/2006"),
				AggregationLevel: aggregation, GroupBy: strings.Join(fields, ","),
				Filter: composeReviewFilter(rawFilter, market), OrderBy: "date", Top: 10000, Skip: 0,
			})
			if err != nil {
				return failureError{err}
			}
			acquisitions := append([]storetypes.Acquisition(nil), page.Value...)
			for page.NextLink != "" {
				page, err = s.client.AppAcquisitionsNext(cmd.Context(), page.NextLink)
				if err != nil {
					return failureError{err}
				}
				acquisitions = append(acquisitions, page.Value...)
			}
			return s.renderAcquisitions(acquisitions, fields)
		},
	}
	command.Flags().StringVar(&from, "from", "", "start date in YYYY-MM-DD (required)")
	command.Flags().StringVar(&to, "to", "", "end date in YYYY-MM-DD, inclusive (default today)")
	command.Flags().StringVar(&aggregation, "aggregation", "day", "day, week or month")
	command.Flags().StringVar(&groupBy, "group-by", "date", "comma-separated breakdown: date, osVersion, market, deviceType, acquisitionType, storeClient, ageGroup, gender, orderName")
	command.Flags().StringVar(&market, "market", "", "market filter")
	command.Flags().StringVar(&rawFilter, "filter", "", "raw Partner Center filter")
	return command
}

// acquisitionGroupBy validates the breakdown and always includes date: without
// it the API still returns a date per row, but rows from different periods
// would be summed into one, which no caller of a per-day report wants.
func acquisitionGroupBy(raw string) ([]string, error) {
	fields := []string{"date"}
	seen := map[string]bool{"date": true}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		canonical, ok := acquisitionGroupFields[strings.ToLower(name)]
		if !ok {
			return nil, fmt.Errorf("unknown field %q", name)
		}
		if !seen[canonical] {
			seen[canonical] = true
			fields = append(fields, canonical)
		}
	}
	return fields, nil
}

func (s *commandState) renderAcquisitions(acquisitions []storetypes.Acquisition, fields []string) error {
	rows := make([]acquisitionRow, 0, len(acquisitions))
	for _, acquisition := range acquisitions {
		rows = append(rows, acquisitionRow{
			Date:                acquisitionDate(acquisition.Date),
			AcquisitionQuantity: int64(math.Round(acquisition.AcquisitionQuantity)),
			OSVersion:           acquisition.OSVersion,
			Market:              acquisition.Market,
			DeviceType:          acquisition.DeviceType,
			AcquisitionType:     acquisition.AcquisitionType,
			StoreClient:         acquisition.StoreClient,
			AgeGroup:            acquisition.AgeGroup,
			Gender:              acquisition.Gender,
			OrderName:           acquisition.OrderName,
		})
	}
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	if s.format == output.JSON {
		return wrapFailure(renderer.Value(rows))
	}
	headers := make([]string, 0, len(fields)+1)
	for _, field := range fields {
		headers = append(headers, strings.ToUpper(field))
	}
	headers = append(headers, "ACQUISITIONS")
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		values := map[string]string{
			"date": row.Date, "osVersion": row.OSVersion, "market": row.Market, "deviceType": row.DeviceType,
			"acquisitionType": row.AcquisitionType, "storeClient": row.StoreClient, "ageGroup": row.AgeGroup,
			"gender": row.Gender, "orderName": row.OrderName,
		}
		line := make([]string, 0, len(headers))
		for _, field := range fields {
			line = append(line, values[field])
		}
		table = append(table, append(line, strconv.FormatInt(row.AcquisitionQuantity, 10)))
	}
	return wrapFailure(renderer.Rows(headers, table))
}

// acquisitionDate normalizes the API's date to YYYY-MM-DD. The documented
// form already is, but the analytics endpoints are inconsistent (reviews
// return "11/10/2026 11:30:10 PM"), so other shapes are parsed too and an
// unrecognized one is passed through rather than dropped.
func acquisitionDate(value string) string {
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "1/2/2006 3:04:05 PM", "1/2/2006"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.Format("2006-01-02")
		}
	}
	return value
}
