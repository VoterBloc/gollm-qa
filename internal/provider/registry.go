package provider

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// DefaultModelSpec is the model spec used when no explicit selection is made.
// Lives in the registry rather than inside any provider implementation so
// the default is visible at the call site rather than buried in a constructor.
const DefaultModelSpec = "claude:sonnet-4-5"

// Factory constructs a provider for a given model name. The name is the
// suffix of a model spec — e.g. for spec "claude:sonnet-4-5" the factory
// receives "sonnet-4-5" and is responsible for resolving aliases or
// passing the value through to its underlying SDK.
type Factory func(model string) (Provider, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register associates a provider prefix (e.g. "claude", "openai") with a
// factory. Provider implementations call this from init() so a blank
// import wires them up.
//
// Calling Register twice with the same prefix panics — provider names
// are global and silent overrides would mask a bug.
func Register(prefix string, f Factory) {
	if prefix == "" {
		panic("provider.Register: empty prefix")
	}
	if f == nil {
		panic("provider.Register: nil factory for " + prefix)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[prefix]; exists {
		panic("provider.Register: duplicate registration for " + prefix)
	}
	registry[prefix] = f
}

// New constructs a provider from a "<provider>:<model>" spec string.
// Returns an error with the registered prefixes listed when the prefix
// isn't recognized — saves the next "what providers do I have" round trip.
func New(spec string) (Provider, error) {
	prefix, model, ok := strings.Cut(spec, ":")
	if !ok || prefix == "" || model == "" {
		return nil, fmt.Errorf("provider: invalid model spec %q (expected '<provider>:<model>')", spec)
	}
	registryMu.RLock()
	f, ok := registry[prefix]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider: unknown provider %q (registered: %v)", prefix, RegisteredPrefixes())
	}
	return f(model)
}

// MustNew is like New but panics on error. Use only for spec strings that
// are programmer-known to be valid (e.g. DefaultModelSpec) — a failure
// here means the corresponding provider package wasn't imported.
func MustNew(spec string) Provider {
	p, err := New(spec)
	if err != nil {
		panic(err)
	}
	return p
}

// RegisteredPrefixes returns the registered provider prefixes in sorted
// order. Stable output is the only thing that lets the error message in
// New be reliably tested.
func RegisteredPrefixes() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
