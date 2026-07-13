package output_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/prof18/pcenter-cli/internal/output"
)

func TestResolveFormatUsesExplicitValueThenTTYDefault(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		explicit string
		isTTY    bool
		want     output.Format
	}{
		{explicit: "json", isTTY: true, want: output.JSON},
		{explicit: "table", isTTY: false, want: output.Table},
		{isTTY: true, want: output.Table},
		{isTTY: false, want: output.JSON},
	} {
		got, err := output.ResolveFormat(test.explicit, test.isTTY)
		if err != nil || got != test.want {
			t.Fatalf("ResolveFormat(%q, %t) = %q, %v; want %q", test.explicit, test.isTTY, got, err, test.want)
		}
	}
	if _, err := output.ResolveFormat("xml", false); err == nil {
		t.Fatal("invalid output format accepted")
	}
}

func TestRendererWritesJSONAndTable(t *testing.T) {
	t.Parallel()
	var jsonBuffer bytes.Buffer
	jsonRenderer := output.NewRenderer(&jsonBuffer, output.JSON)
	if err := jsonRenderer.Value(map[string]string{"status": "ok"}); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(jsonBuffer.Bytes(), &decoded); err != nil || decoded["status"] != "ok" {
		t.Fatalf("JSON output = %q, error = %v", jsonBuffer.String(), err)
	}

	var tableBuffer bytes.Buffer
	tableRenderer := output.NewRenderer(&tableBuffer, output.Table)
	if err := tableRenderer.Rows([]string{"NAME", "STATUS"}, [][]string{{"Example", "Published"}}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"NAME", "STATUS", "Example", "Published"} {
		if !strings.Contains(tableBuffer.String(), expected) {
			t.Fatalf("table output missing %q: %s", expected, tableBuffer.String())
		}
	}
}

func TestWriteErrorUsesOneLineJSON(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	output.WriteError(&buffer, output.JSON, errors.New("broken"))
	if strings.Count(buffer.String(), "\n") != 1 || !json.Valid(bytes.TrimSpace(buffer.Bytes())) {
		t.Fatalf("error output is not one-line JSON: %q", buffer.String())
	}
	if !strings.Contains(buffer.String(), "broken") {
		t.Fatalf("error missing message: %q", buffer.String())
	}
}

func TestExitCodeConventions(t *testing.T) {
	t.Parallel()
	if output.ExitSuccess != 0 || output.ExitFailure != 1 || output.ExitUsage != 2 {
		t.Fatalf("unexpected exit codes: %d %d %d", output.ExitSuccess, output.ExitFailure, output.ExitUsage)
	}
}
