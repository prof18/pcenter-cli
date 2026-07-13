// Package output implements the CLI's JSON/table conventions and exit codes.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

const (
	// ExitSuccess indicates successful command completion.
	ExitSuccess = 0
	// ExitFailure indicates an API, authentication, validation, or runtime failure.
	ExitFailure = 1
	// ExitUsage indicates invalid CLI usage or missing configuration.
	ExitUsage = 2
)

// Format is a supported CLI output representation.
type Format string

const (
	// JSON writes machine-readable JSON.
	JSON Format = "json"
	// Table writes a human-readable table.
	Table Format = "table"
)

// ResolveFormat validates an explicit format or chooses a TTY-aware default.
func ResolveFormat(explicit string, isTTY bool) (Format, error) {
	switch strings.ToLower(explicit) {
	case "json":
		return JSON, nil
	case "table":
		return Table, nil
	case "":
		if isTTY {
			return Table, nil
		}
		return JSON, nil
	default:
		return "", fmt.Errorf("invalid output format %q: expected json or table", explicit)
	}
}

// IsTerminal reports whether file is a character device.
func IsTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Renderer writes command results.
type Renderer struct {
	w      io.Writer
	format Format
}

// NewRenderer creates a result renderer.
func NewRenderer(writer io.Writer, format Format) Renderer {
	return Renderer{w: writer, format: format}
}

// Format returns the renderer's selected format.
func (r Renderer) Format() Format { return r.format }

// Value writes one JSON value. Table callers should prefer Rows.
func (r Renderer) Value(value any) error {
	encoder := json.NewEncoder(r.w)
	if r.format == Table {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(value)
}

// RawJSON writes validated JSON followed by one newline.
func (r Renderer) RawJSON(value []byte) error {
	if !json.Valid(value) {
		return fmt.Errorf("invalid JSON result")
	}
	_, err := fmt.Fprintf(r.w, "%s\n", value)
	return err
}

// Rows writes aligned table rows. JSON commands should render their typed values with Value.
func (r Renderer) Rows(headers []string, rows [][]string) error {
	writer := tabwriter.NewWriter(r.w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(writer, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return writer.Flush()
}

// WriteError follows the one-line JSON convention when JSON output is selected.
func WriteError(writer io.Writer, format Format, err error) {
	if format == JSON {
		_ = json.NewEncoder(writer).Encode(map[string]string{"error": err.Error()})
		return
	}
	_, _ = fmt.Fprintln(writer, "Error:", err)
}
