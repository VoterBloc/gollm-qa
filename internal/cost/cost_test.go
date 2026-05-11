package cost_test

import (
	"bytes"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VoterBloc/gollm-qa/internal/cost"
	"github.com/VoterBloc/gollm-qa/internal/provider"
)

func TestEstimate_KnownModel(t *testing.T) {
	tbl := cost.LoadDefaults()

	// 1M input + 1M output at the shipped rates ($3 + $15) = $18.
	got := tbl.Estimate(provider.Usage{
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
		ModelID:      "claude:sonnet-4-5",
	})
	if math.Abs(got-18.00) > 1e-9 {
		t.Errorf("Estimate = %v, want 18.00", got)
	}
}

func TestEstimate_PartialCounts(t *testing.T) {
	tbl := cost.LoadDefaults()

	// 100k input ($0.30) + 50k output ($0.75) = $1.05.
	got := tbl.Estimate(provider.Usage{
		InputTokens:  100_000,
		OutputTokens: 50_000,
		ModelID:      "claude:sonnet-4-5",
	})
	want := 1.05
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("Estimate = %v, want %v", got, want)
	}
}

func TestEstimate_UnknownModelLogsOnce(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	tbl := cost.LoadDefaults().WithLogger(logger)

	usage := provider.Usage{
		InputTokens:  100,
		OutputTokens: 200,
		ModelID:      "loch-ness:plesiosaur-9000",
	}
	for i := 0; i < 5; i++ {
		got := tbl.Estimate(usage)
		if got != 0 {
			t.Errorf("call %d: Estimate = %v, want 0 for unknown model", i, got)
		}
	}

	occurrences := strings.Count(buf.String(), "loch-ness:plesiosaur-9000")
	if occurrences != 1 {
		t.Errorf("expected 1 warning log line for unknown model, got %d:\n%s", occurrences, buf.String())
	}
}

func TestWithLogger_SharesDedupeStateWithOriginal(t *testing.T) {
	// First Estimate via the original Table consumes the warning slot.
	// The Table returned by WithLogger should see "already warned" and
	// stay quiet, even though it has a different logger attached.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	original := cost.LoadDefaults().WithLogger(logger)
	usage := provider.Usage{
		InputTokens:  1,
		OutputTokens: 1,
		ModelID:      "yeti:himalayan-7",
	}
	original.Estimate(usage)
	if got := strings.Count(buf.String(), "yeti:himalayan-7"); got != 1 {
		t.Fatalf("setup: expected 1 warning on original, got %d", got)
	}

	// Swap to a fresh logger; the dedupe state must follow.
	var buf2 bytes.Buffer
	logger2 := slog.New(slog.NewTextHandler(&buf2, &slog.HandlerOptions{Level: slog.LevelWarn}))
	rerouted := original.WithLogger(logger2)
	rerouted.Estimate(usage)

	if got := strings.Count(buf2.String(), "yeti:himalayan-7"); got != 0 {
		t.Errorf("expected dedupe to suppress re-warn after WithLogger, got %d warnings:\n%s", got, buf2.String())
	}
}

func TestEstimate_EmptyModelIDReturnsZero(t *testing.T) {
	tbl := cost.LoadDefaults()
	got := tbl.Estimate(provider.Usage{
		InputTokens:  500,
		OutputTokens: 1000,
		ModelID:      "",
	})
	if got != 0 {
		t.Errorf("Estimate with empty ModelID = %v, want 0", got)
	}
}

func TestLoad_OverrideMergesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "override.yaml")
	body := []byte(`
providers:
  claude:
    sonnet-4-5:
      input_per_million_usd: 99.99
      output_per_million_usd: 88.88
  bigfoot:
    sasquatch-9000:
      input_per_million_usd: 1.00
      output_per_million_usd: 2.00
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	tbl, err := cost.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Default got replaced.
	got := tbl.Estimate(provider.Usage{
		InputTokens:  1_000_000,
		OutputTokens: 0,
		ModelID:      "claude:sonnet-4-5",
	})
	if math.Abs(got-99.99) > 1e-9 {
		t.Errorf("override claude:sonnet-4-5 input rate not applied: got %v want 99.99", got)
	}

	// New entry added.
	if !tbl.Has("bigfoot:sasquatch-9000") {
		t.Error("expected override to add bigfoot:sasquatch-9000 to the table")
	}
	got = tbl.Estimate(provider.Usage{
		InputTokens:  2_000_000,
		OutputTokens: 1_000_000,
		ModelID:      "bigfoot:sasquatch-9000",
	})
	want := 4.00 // 2M * $1 + 1M * $2 per million.
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("bigfoot estimate = %v, want %v", got, want)
	}
}

func TestEstimate_WildcardCoversUnlistedModel(t *testing.T) {
	// local:* in the shipped defaults should cover any model name
	// without firing the unknown-model warning.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	tbl := cost.LoadDefaults().WithLogger(logger)

	for _, id := range []string{"local:qwen2.5", "local:phi3", "local:mistral", "local:deepseek-r1"} {
		got := tbl.Estimate(provider.Usage{
			InputTokens:  1_000_000,
			OutputTokens: 1_000_000,
			ModelID:      id,
		})
		if got != 0 {
			t.Errorf("Estimate(%s) = %v, want 0 (wildcard zero-cost)", id, got)
		}
		if !tbl.Has(id) {
			t.Errorf("Has(%s) = false, want true via wildcard coverage", id)
		}
	}

	if strings.Contains(buf.String(), "unknown model id") {
		t.Errorf("wildcard should suppress the unknown-model warning, got:\n%s", buf.String())
	}
}

func TestEstimate_ExactEntryWinsOverWildcard(t *testing.T) {
	// Override file pins local:qwen2.5 at non-zero while local:* is
	// still zero in defaults. Exact match must win — otherwise users
	// can't pin individual models in a wildcard'd provider.
	dir := t.TempDir()
	path := filepath.Join(dir, "override.yaml")
	body := []byte(`
providers:
  local:
    qwen2.5:
      input_per_million_usd: 5.00
      output_per_million_usd: 10.00
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	tbl, err := cost.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Exact entry wins.
	got := tbl.Estimate(provider.Usage{
		InputTokens:  1_000_000,
		OutputTokens: 0,
		ModelID:      "local:qwen2.5",
	})
	if got != 5.00 {
		t.Errorf("Estimate(local:qwen2.5) = %v, want 5.00 (exact entry wins)", got)
	}

	// Other local:* still hits the wildcard at zero.
	got = tbl.Estimate(provider.Usage{
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
		ModelID:      "local:phi3",
	})
	if got != 0 {
		t.Errorf("Estimate(local:phi3) = %v, want 0 (wildcard fallback)", got)
	}
}

func TestLoad_EmptyPathReturnsDefaults(t *testing.T) {
	tbl, err := cost.Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if !tbl.Has("claude:sonnet-4-5") {
		t.Error("expected defaults to include claude:sonnet-4-5")
	}
}

func TestLoad_MissingFileErrors(t *testing.T) {
	if _, err := cost.Load("/nonexistent/fishsticks.yaml"); err == nil {
		t.Error("expected Load to error on missing file")
	}
}

func TestLoad_EmptyProvidersErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shape-mistake.yaml")
	// Wrong top-level key — common shape mistake (e.g. dropping the
	// "providers:" wrapper). Should fail loudly, not silently fall back.
	body := []byte(`
claude:
  sonnet-4-5:
    input_per_million_usd: 99.99
    output_per_million_usd: 88.88
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := cost.Load(path)
	if err == nil {
		t.Fatal("expected error for pricing file with no providers")
	}
	if !strings.Contains(err.Error(), "no providers") {
		t.Errorf("error %q missing 'no providers' guidance", err)
	}
}

func TestLoad_BadYAMLErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("::: not valid yaml :::"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cost.Load(path); err == nil {
		t.Error("expected Load to error on malformed YAML")
	}
}

func TestDefaults_HasExpectedModels(t *testing.T) {
	tbl := cost.LoadDefaults()
	// At minimum, the model spec the registry defaults to must be priced —
	// otherwise a fresh `gollm run` reports zero cost for the happy path.
	if !tbl.Has(provider.DefaultModelSpec) {
		t.Errorf("defaults missing pricing for %q (the registry default)", provider.DefaultModelSpec)
	}
	// Each provider that lands in the epic should ship priced — otherwise
	// the unknown-model warning fires on first request and Estimated USD
	// reports zero. Add a model here when its provider package lands.
	mustBePriced := []string{
		"openai:gpt-4o",
		"openai:gpt-4o-mini",
		"gemini:2.5-pro",
		"gemini:2.5-flash",
		// Any local:* should hit the wildcard entry — no explicit
		// listing per model required. Picking llama3.1 here as the
		// common-case proof; the wildcard mechanic is exercised
		// separately by TestEstimate_WildcardCoversUnlistedModel.
		"local:llama3.1",
	}
	for _, id := range mustBePriced {
		if !tbl.Has(id) {
			t.Errorf("defaults missing pricing for %q", id)
		}
	}
}
