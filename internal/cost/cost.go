// Package cost turns provider.Usage into USD via a pricing table.
//
// Pricing keys are "<provider>:<model>" specs — the same form
// provider.New accepts and stamps onto provider.Usage.ModelID — so the
// agent loop can call Estimate without translating the model identifier.
//
// Defaults ship embedded; user overrides come from a YAML file with the
// same shape and merge per-key (override wins, no deep merge).
package cost

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/VoterBloc/gollm-qa/internal/provider"
)

//go:embed models.yaml
var defaultsYAML []byte

// Price is the per-million-token rate for a single model.
type Price struct {
	InputPerMillionUSD  float64 `yaml:"input_per_million_usd"`
	OutputPerMillionUSD float64 `yaml:"output_per_million_usd"`
}

// fileShape is the on-disk layout. Nesting under "providers:" and then
// the provider prefix avoids quoting "claude:sonnet-4-5" as a YAML key
// (the bare colon would otherwise be parsed as a mapping).
type fileShape struct {
	Providers map[string]map[string]Price `yaml:"providers"`
}

// Table looks up USD-per-million rates by spec key.
type Table struct {
	prices map[string]Price

	// warnedUnknown deduplicates "unknown model" log lines across
	// concurrent agents — the issue calls for "logs once" per id.
	// Pointer so WithLogger can share state with the original Table
	// without value-copying a sync.Map (which is unsafe after first use).
	warnedUnknown *sync.Map // map[string]struct{}

	logger *slog.Logger
}

// LoadDefaults returns a Table populated from the embedded defaults.
// Panics on parse failure — that's a build-time mistake in this repo's
// own data, not user input.
func LoadDefaults() *Table {
	t, err := parse(defaultsYAML)
	if err != nil {
		panic(fmt.Sprintf("cost: invalid embedded defaults: %v", err))
	}
	return t
}

// Load returns a Table merging the embedded defaults with the YAML
// file at path. Empty path returns defaults only. Override entries
// replace defaults at the spec-key level (no deep merge of Price).
func Load(path string) (*Table, error) {
	t := LoadDefaults()
	if path == "" {
		return t, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading pricing file %s: %w", path, err)
	}
	override, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("parsing pricing file %s: %w", path, err)
	}
	// A non-empty file that yields zero prices is almost certainly a
	// shape mistake (top-level "providers:" missing, indentation off,
	// wrong key names). Better to fail loudly than silently fall back
	// to defaults and report stale costs.
	if len(override.prices) == 0 && len(data) > 0 {
		return nil, fmt.Errorf("pricing file %s parsed but contained no providers (check the top-level 'providers:' key)", path)
	}
	for k, v := range override.prices {
		t.prices[k] = v
	}
	return t, nil
}

func parse(data []byte) (*Table, error) {
	var f fileShape
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	prices := make(map[string]Price)
	for prefix, models := range f.Providers {
		for model, price := range models {
			prices[prefix+":"+model] = price
		}
	}
	return &Table{
		prices:        prices,
		warnedUnknown: &sync.Map{},
		logger:        slog.Default(),
	}, nil
}

// WithLogger returns a copy of t that logs the unknown-model warning
// to lg. Useful for tests that want to silence the warning, or for
// callers that route logs to a structured handler.
//
// Both Tables share the same dedupe state, so an unknown id warned via
// one Table won't re-warn through the other — keeps the "logs once" guarantee
// stable across logger swaps. prices is read-only after Load, so sharing
// the map pointer is safe too.
func (t *Table) WithLogger(lg *slog.Logger) *Table {
	if lg == nil {
		lg = slog.Default()
	}
	return &Table{
		prices:        t.prices,
		warnedUnknown: t.warnedUnknown,
		logger:        lg,
	}
}

// Estimate returns the USD cost of a request given its token counts and
// model id. Returns 0 (and logs once) for unknown ids — cost accounting
// degrades gracefully rather than failing the run.
func (t *Table) Estimate(u provider.Usage) float64 {
	if u.ModelID == "" {
		return 0
	}
	p, ok := t.prices[u.ModelID]
	if !ok {
		if t.warnedUnknown != nil {
			if _, loaded := t.warnedUnknown.LoadOrStore(u.ModelID, struct{}{}); !loaded {
				t.logger.Warn("cost: unknown model id, treating as zero cost", "model_id", u.ModelID)
			}
		}
		return 0
	}
	const perMillion = 1_000_000.0
	return float64(u.InputTokens)*p.InputPerMillionUSD/perMillion +
		float64(u.OutputTokens)*p.OutputPerMillionUSD/perMillion
}

// Has reports whether t has a price for the given spec id. Useful for
// callers that want to validate a model selection at startup rather
// than discovering missing pricing on the first response.
func (t *Table) Has(modelID string) bool {
	_, ok := t.prices[modelID]
	return ok
}
