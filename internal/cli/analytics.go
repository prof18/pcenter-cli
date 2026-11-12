package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/prof18/pcenter-cli/internal/output"
)

// analyticsSpec describes one Store analytics endpoint. The endpoints share
// paging, dates and filters but differ in their dimensions and metrics, so
// each command is a spec run by the same code rather than its own copy.
type analyticsSpec struct {
	use, short, long string
	endpoint         string
	// dimensions are the fields --group-by accepts, spelled as the API does.
	// Empty means the endpoint takes no groupby.
	dimensions     []string
	defaultGroupBy string
	// dateless endpoints (funnel) return no date per row, so none is grouped
	// by or reported.
	dateless bool
	// identity fields are always kept when present: without them, rows from
	// different add-ons could not be told apart.
	identity []string
	metrics  []string
	// aggregations lists the accepted --aggregation values, the default
	// first. Empty means the endpoint has no aggregationLevel.
	aggregations []string
	// passthrough keeps every non-null field in API order, for endpoints whose
	// rows are records rather than aggregates (failure details, stack frames).
	passthrough bool
	// noDateRange is for endpoints keyed by an id alone (stack trace).
	noDateRange bool
	// noPaging is for endpoints that take no top/skip (funnel, insights).
	noPaging bool
	// dataTypes, when set, replaces --market/--filter with a --data-type flag
	// composed into the filter the endpoint requires (insights).
	dataTypes []string
	orderBy   string
	top       int
	params    []analyticsParam
	// rename maps an API field to the name pcenter reports, for endpoints
	// that break the API's own camelCase convention.
	rename map[string]string
	// fix rewrites a reported value the API is known to misspell.
	fix func(field, value string) string
	// window is the history limit the API enforces, for the help text.
	window string
}

// analyticsParam is an endpoint-specific id flag passed as a query parameter.
type analyticsParam struct {
	flag, query, usage string
	required           bool
}

func (s *commandState) analyticsCommand() *cobra.Command {
	parent := &cobra.Command{Use: "analytics", Short: "Read Store analytics"}
	for _, spec := range analyticsSpecs() {
		parent.AddCommand(s.analyticsSpecCommand(spec))
	}
	for _, group := range analyticsGroups() {
		command := &cobra.Command{Use: group.use, Short: group.short}
		for _, spec := range group.specs {
			command.AddCommand(s.analyticsSpecCommand(spec))
		}
		parent.AddCommand(command)
	}
	return parent
}

func (s *commandState) analyticsSpecCommand(spec analyticsSpec) *cobra.Command {
	var from, to, aggregation, groupBy, market, rawFilter, dataType string
	ids := make([]string, len(spec.params))
	long := spec.long
	if spec.window != "" {
		long += "\n\nThe API only returns " + spec.window + "."
	}
	command := &cobra.Command{
		Use:   spec.use,
		Short: spec.short,
		Long:  long,
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := url.Values{}
			var lastDay string
			if !spec.noDateRange {
				startDate, endDate, last, err := analyticsDateRange(from, to, s.dependencies.Now())
				if err != nil {
					return err
				}
				query.Set("startDate", startDate)
				query.Set("endDate", endDate)
				lastDay = last
			}
			for i, param := range spec.params {
				value := strings.TrimSpace(ids[i])
				if value == "" {
					if param.required {
						return usageError{fmt.Errorf("--%s is required", param.flag)}
					}
					continue
				}
				query.Set(param.query, value)
			}
			if len(spec.aggregations) > 0 {
				if !containsString(spec.aggregations, aggregation) {
					return usageError{fmt.Errorf("--aggregation must be one of %s", strings.Join(spec.aggregations, ", "))}
				}
				query.Set("aggregationLevel", aggregation)
			}
			var fields []string
			if len(spec.dimensions) > 0 {
				var err error
				fields, err = analyticsGroupBy(spec, groupBy)
				if err != nil {
					return usageError{fmt.Errorf("--group-by: %w", err)}
				}
				query.Set("groupby", strings.Join(fields, ","))
			}
			if len(spec.dataTypes) > 0 {
				filter, err := dataTypeFilter(spec.dataTypes, dataType)
				if err != nil {
					return usageError{fmt.Errorf("--data-type: %w", err)}
				}
				query.Set("filter", filter)
			} else if filter := composeReviewFilter(rawFilter, market); filter != "" {
				query.Set("filter", filter)
			}
			if spec.orderBy != "" {
				query.Set("orderby", spec.orderBy)
			}
			if !spec.noDateRange && !spec.noPaging {
				top := spec.top
				if top == 0 {
					top = 10000
				}
				query.Set("top", strconv.Itoa(top))
				query.Set("skip", "0")
			}
			if err := s.prepareClient(); err != nil {
				return err
			}
			query.Set("applicationId", s.config.AppID)
			page, err := s.client.Analytics(cmd.Context(), spec.endpoint, query)
			if err != nil {
				return failureError{err}
			}
			raw := append([]json.RawMessage(nil), page.Value...)
			for page.NextLink != "" {
				page, err = s.client.AnalyticsNext(cmd.Context(), page.NextLink)
				if err != nil {
					return failureError{err}
				}
				raw = append(raw, page.Value...)
			}
			rows, err := analyticsRows(spec, fields, raw)
			if err != nil {
				return failureError{err}
			}
			if !spec.noDateRange && !spec.dateless {
				rows = sortAndTrimByDate(rows, lastDay)
			}
			return s.renderAnalytics(rows)
		},
	}
	flags := command.Flags()
	if !spec.noDateRange {
		flags.StringVar(&from, "from", "", "start date in YYYY-MM-DD (required)")
		flags.StringVar(&to, "to", "", "end date in YYYY-MM-DD, inclusive (default today)")
	}
	if len(spec.dataTypes) > 0 {
		flags.StringVar(&dataType, "data-type", strings.Join(spec.dataTypes, ","), "comma-separated: "+strings.Join(spec.dataTypes, ", "))
	} else if !spec.noDateRange {
		flags.StringVar(&market, "market", "", "market filter")
		flags.StringVar(&rawFilter, "filter", "", "raw Partner Center filter")
	}
	if len(spec.aggregations) > 0 {
		flags.StringVar(&aggregation, "aggregation", spec.aggregations[0], strings.Join(spec.aggregations, ", "))
	}
	if len(spec.dimensions) > 0 {
		usage := "comma-separated breakdown: " + strings.Join(spec.dimensions, ", ")
		flags.StringVar(&groupBy, "group-by", spec.defaultGroupBy, usage)
	}
	for i, param := range spec.params {
		usage := param.usage
		if param.required {
			usage += " (required)"
		}
		flags.StringVar(&ids[i], param.flag, "", usage)
	}
	return command
}

func dataTypeFilter(allowed []string, raw string) (string, error) {
	var statements []string
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if !containsString(allowed, name) {
			return "", fmt.Errorf("unknown type %q", name)
		}
		statements = append(statements, "dataType eq '"+name+"'")
	}
	if len(statements) == 0 {
		return "", errors.New("at least one type is required")
	}
	return strings.Join(statements, " or "), nil
}

// analyticsDateRange returns the API's start and end dates and the last day
// to report, as YYYY-MM-DD.
func analyticsDateRange(from, to string, now time.Time) (string, string, string, error) {
	if strings.TrimSpace(from) == "" {
		return "", "", "", usageError{errors.New("--from is required")}
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return "", "", "", usageError{errors.New("--from: expected YYYY-MM-DD")}
	}
	end := now.UTC()
	if to != "" {
		end, err = time.Parse("2006-01-02", to)
		if err != nil {
			return "", "", "", usageError{errors.New("--to: expected YYYY-MM-DD")}
		}
	}
	if end.Format("2006-01-02") < start.Format("2006-01-02") {
		return "", "", "", usageError{errors.New("--to must not be before --from")}
	}
	return start.Format("1/2/2006"), end.Format("1/2/2006"), end.Format("2006-01-02"), nil
}

// sortAndTrimByDate orders rows by date, keeping the API's order within a
// date, and drops rows dated after lastDay. Both are needed because the
// endpoints disagree: installs and usage ignore orderby and return newest
// first, and they include a row for the day after endDate. Rows before --from
// are kept, since a week or month row is dated by the start of its period.
func sortAndTrimByDate(rows []analyticsRow, lastDay string) []analyticsRow {
	kept := rows[:0]
	for _, row := range rows {
		if day := rowDay(row); day == "" || day <= lastDay {
			kept = append(kept, row)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return rowDay(kept[i]) < rowDay(kept[j]) })
	return kept
}

func rowDay(row analyticsRow) string {
	var date string
	if err := json.Unmarshal(row.values["date"], &date); err != nil || len(date) < 10 {
		return ""
	}
	return date[:10]
}

// analyticsGroupBy validates the breakdown, case-insensitively, and always
// includes date for a dated endpoint: without it the API still returns a date
// per row, but rows from different periods are summed into one, which no
// caller of a per-day report wants.
func analyticsGroupBy(spec analyticsSpec, raw string) ([]string, error) {
	var fields []string
	seen := map[string]bool{}
	if !spec.dateless {
		fields = append(fields, "date")
		seen["date"] = true
	}
	canonical := make(map[string]string, len(spec.dimensions))
	for _, dimension := range spec.dimensions {
		canonical[strings.ToLower(dimension)] = dimension
	}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if strings.EqualFold(name, "date") && !spec.dateless {
			continue
		}
		field, ok := canonical[strings.ToLower(name)]
		if !ok {
			return nil, fmt.Errorf("unknown field %q", name)
		}
		if !seen[field] {
			seen[field] = true
			fields = append(fields, field)
		}
	}
	return fields, nil
}

// analyticsRow is one reported row with its fields in a fixed order, so JSON
// and table output list them the same way every time.
type analyticsRow struct {
	keys   []string
	values map[string]json.RawMessage
}

func (r analyticsRow) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for i, key := range r.keys {
		if i > 0 {
			buffer.WriteByte(',')
		}
		name, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buffer.Write(name)
		buffer.WriteByte(':')
		buffer.Write(r.values[key])
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// analyticsRows selects and normalizes the reported fields. The API returns
// every field of the endpoint on every row, with null or "All" for the
// dimensions that were not grouped by, so an aggregate row keeps only date,
// the requested dimensions, identity fields and metrics.
func analyticsRows(spec analyticsSpec, fields []string, raw []json.RawMessage) ([]analyticsRow, error) {
	wanted := append(append(append([]string(nil), fields...), spec.identity...), spec.metrics...)
	rows := make([]analyticsRow, 0, len(raw))
	for _, item := range raw {
		keys, values, err := decodeOrdered(item)
		if err != nil {
			return nil, fmt.Errorf("decode %s row: %w", spec.endpoint, err)
		}
		if !spec.passthrough {
			keys = wanted
		}
		row := analyticsRow{values: map[string]json.RawMessage{}}
		for _, key := range keys {
			value, ok := values[key]
			if !ok || string(value) == "null" {
				continue
			}
			name := key
			if renamed, ok := spec.rename[key]; ok {
				name = renamed
			}
			row.keys = append(row.keys, name)
			row.values[name] = normalizeAnalyticsValue(spec, name, value)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// decodeOrdered reads a JSON object keeping its key order, for passthrough
// rows where the API's own order is the most readable one.
func decodeOrdered(raw json.RawMessage) ([]string, map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, nil, errors.New("row is not an object")
	}
	var keys []string
	values := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, nil, errors.New("row key is not a string")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		if _, seen := values[key]; !seen {
			keys = append(keys, key)
		}
		values[key] = value
	}
	return keys, values, nil
}

// normalizeAnalyticsValue gives dates one format and whole numbers no
// fraction. The API sends counts as 2.0 in some endpoints and 2 in others;
// reporting both as 2 lets callers treat them as integers.
func normalizeAnalyticsValue(spec analyticsSpec, field string, value json.RawMessage) json.RawMessage {
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		if field == "date" {
			text = analyticsDate(text)
		}
		if spec.fix != nil {
			text = spec.fix(field, text)
		}
		encoded, err := json.Marshal(text)
		if err != nil {
			return value
		}
		return encoded
	}
	var number float64
	if err := json.Unmarshal(value, &number); err == nil && number == math.Trunc(number) && math.Abs(number) < 1<<53 {
		return json.RawMessage(strconv.FormatInt(int64(number), 10))
	}
	return value
}

// analyticsDate normalizes the API's dates. The endpoints are inconsistent:
// "2026-11-10", "2026-11-10 00:00:00", "2026-11-10T00:00:00", or (reviews)
// "11/10/2026 11:30:10 PM". A midnight timestamp becomes YYYY-MM-DD; a real
// time (hourly failures) keeps it as YYYY-MM-DDTHH:MM:SS. An unrecognized
// value is passed through rather than dropped.
func analyticsDate(value string) string {
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04:05.0000000Z", "1/2/2006 3:04:05 PM", "1/2/2006"} {
		parsed, err := time.Parse(layout, value)
		if err != nil {
			continue
		}
		if parsed.Hour() == 0 && parsed.Minute() == 0 && parsed.Second() == 0 {
			return parsed.Format("2006-01-02")
		}
		return parsed.Format("2006-01-02T15:04:05")
	}
	return value
}

func (s *commandState) renderAnalytics(rows []analyticsRow) error {
	renderer := output.NewRenderer(s.dependencies.Stdout, s.format)
	if s.format == output.JSON {
		return wrapFailure(renderer.Value(rows))
	}
	var headers []string
	seen := map[string]bool{}
	for _, row := range rows {
		for _, key := range row.keys {
			if !seen[key] {
				seen[key] = true
				headers = append(headers, key)
			}
		}
	}
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		line := make([]string, 0, len(headers))
		for _, header := range headers {
			value, ok := row.values[header]
			if !ok {
				line = append(line, "")
				continue
			}
			var text string
			if err := json.Unmarshal(value, &text); err == nil {
				line = append(line, truncate(text, 80))
			} else {
				line = append(line, truncate(string(value), 80))
			}
		}
		table = append(table, line)
	}
	upper := make([]string, len(headers))
	for i, header := range headers {
		upper[i] = strings.ToUpper(header)
	}
	return wrapFailure(renderer.Rows(upper, table))
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
