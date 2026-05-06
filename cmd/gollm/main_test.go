package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintPurgeReport_TableShape(t *testing.T) {
	report := `{"byTable":[{"table":"bigfoot_sightings","deleted":7},{"table":"chupacabra_sightings","deleted":3}],"total":10}`

	var buf bytes.Buffer
	printPurgeReport(&buf, report)
	out := buf.String()

	for _, want := range []string{
		"TABLE",
		"DELETED",
		"bigfoot_sightings",
		"chupacabra_sightings",
		"TOTAL",
		"10",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}

	// Per-table counts should appear next to their table name.
	bigfootLine := lineContaining(out, "bigfoot_sightings")
	if !strings.Contains(bigfootLine, "7") {
		t.Errorf("expected bigfoot row to show count 7, got: %q", bigfootLine)
	}
}

func TestPrintPurgeReport_PrettyJSONFallback(t *testing.T) {
	// Valid JSON but without a byTable array — falls back to pretty JSON.
	report := `{"summary":"Sasquatch eluded the purge","status":"OK"}`

	var buf bytes.Buffer
	printPurgeReport(&buf, report)
	out := buf.String()

	if !strings.Contains(out, "Sasquatch eluded the purge") {
		t.Errorf("expected raw value preserved, got: %s", out)
	}
	// Pretty-printed JSON has indented keys.
	if !strings.Contains(out, `  "summary"`) {
		t.Errorf("expected pretty-printed indentation, got: %s", out)
	}
}

func TestPrintPurgeReport_RawStringFallback(t *testing.T) {
	// Not parseable as JSON at all — dumped verbatim.
	report := "not even close to JSON, it's a haiku about Mothman"

	var buf bytes.Buffer
	printPurgeReport(&buf, report)
	out := buf.String()

	if !strings.Contains(out, "haiku about Mothman") {
		t.Errorf("expected raw string preserved, got: %s", out)
	}
}

func TestPrintPurgeReport_EmptyByTable(t *testing.T) {
	// Empty array is still the table shape — should render headers + zero total.
	report := `{"byTable":[],"total":0}`

	var buf bytes.Buffer
	printPurgeReport(&buf, report)
	out := buf.String()

	if !strings.Contains(out, "TABLE") {
		t.Errorf("expected table header, got: %s", out)
	}
	if !lineExactlyContaining(out, "TOTAL", "0") {
		t.Errorf("expected TOTAL 0 row, got: %s", out)
	}
}

func lineContaining(s, sub string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}

func lineExactlyContaining(s string, parts ...string) bool {
	for _, line := range strings.Split(s, "\n") {
		match := true
		for _, p := range parts {
			if !strings.Contains(line, p) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
