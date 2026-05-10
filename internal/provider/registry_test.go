package provider_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/VoterBloc/gollm-qa/internal/provider"
	_ "github.com/VoterBloc/gollm-qa/internal/provider/claude" // exercise the real registration
	_ "github.com/VoterBloc/gollm-qa/internal/provider/gemini" // exercise the real registration
	_ "github.com/VoterBloc/gollm-qa/internal/provider/openai" // exercise the real registration
)

// stubProvider is a no-op Provider used to verify registry plumbing
// without making real API calls.
type stubProvider struct {
	model string
}

func (s *stubProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool) (*provider.Response, error) {
	return &provider.Response{Usage: provider.Usage{ModelID: "stub:" + s.model}}, nil
}

// registerForTest registers a unique-to-this-test prefix and queues
// cleanup so the entry doesn't survive into the next test (or the next
// `go test -count=N` iteration). The registry is a process-global
// singleton — without cleanup, a re-run would panic on duplicate
// registration.
func registerForTest(t *testing.T, prefix string, f provider.Factory) {
	t.Helper()
	provider.Register(prefix, f)
	t.Cleanup(func() { provider.UnregisterForTest(prefix) })
}

func TestNew_RoutesByPrefix(t *testing.T) {
	registerForTest(t, "bigfoot", func(model string) (provider.Provider, error) {
		return &stubProvider{model: model}, nil
	})

	p, err := provider.New("bigfoot:sasquatch-9000")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stub, ok := p.(*stubProvider)
	if !ok {
		t.Fatalf("expected *stubProvider, got %T", p)
	}
	if stub.model != "sasquatch-9000" {
		t.Errorf("factory got model %q, want %q", stub.model, "sasquatch-9000")
	}
}

func TestNew_InvalidSpec(t *testing.T) {
	cases := []string{
		"",
		"no-colon",
		":only-suffix",
		"only-prefix:",
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			_, err := provider.New(spec)
			if err == nil {
				t.Fatalf("New(%q) returned no error", spec)
			}
			if !strings.Contains(err.Error(), "invalid model spec") {
				t.Errorf("error %q missing 'invalid model spec' guidance", err)
			}
		})
	}
}

func TestNew_UnknownProviderListsRegistered(t *testing.T) {
	_, err := provider.New("flat-earth-society:lizardperson-3")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error %q missing 'unknown provider' guidance", err)
	}
	// claude must always be in the listed prefixes — it self-registers
	// via the blank import above, and that's exactly the help we want
	// the user to see.
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error %q does not mention any registered providers", err)
	}
}

func TestNew_FactoryErrorPropagates(t *testing.T) {
	sentinel := errors.New("taco cannon misfire")
	registerForTest(t, "tacocannon", func(_ string) (provider.Provider, error) {
		return nil, sentinel
	})

	_, err := provider.New("tacocannon:soft-shell-mk2")
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}

func TestDefaultModelSpec_Resolves(t *testing.T) {
	// The default must resolve as long as the claude package is imported —
	// blank-imported at the top of this file. Anything else is a setup bug
	// callers will hit at startup.
	p, err := provider.New(provider.DefaultModelSpec)
	if err != nil {
		t.Fatalf("New(DefaultModelSpec=%q): %v", provider.DefaultModelSpec, err)
	}
	if p == nil {
		t.Fatal("New returned nil provider")
	}
}

func TestNew_OpenAISpecResolves(t *testing.T) {
	// Pinned: as long as the openai package is blank-imported at the top
	// of this file, "openai:gpt-4o" must produce a working provider.
	// Catches the regression where the openai package's init drifts away
	// from "openai" or the spec parser stops accepting hyphens in models.
	p, err := provider.New("openai:gpt-4o")
	if err != nil {
		t.Fatalf("New(\"openai:gpt-4o\"): %v", err)
	}
	if p == nil {
		t.Fatal("New returned nil provider")
	}
}

func TestNew_GeminiSpecResolves(t *testing.T) {
	// Unlike the anthropic / openai SDKs which validate credentials
	// at first-request time, google.golang.org/genai validates at
	// client construction. The registry probe just wants to confirm
	// the prefix is wired — supply a fake key via env so NewClient
	// doesn't reject before we get to the registration check.
	t.Setenv("GEMINI_API_KEY", "sk-fake-test-key")

	p, err := provider.New("gemini:2.5-pro")
	if err != nil {
		t.Fatalf("New(\"gemini:2.5-pro\"): %v", err)
	}
	if p == nil {
		t.Fatal("New returned nil provider")
	}
}

func TestMustNew_PanicsOnUnknownProvider(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected MustNew to panic on unknown provider")
		}
	}()
	provider.MustNew("definitely-not-a-real-senator:gpt-9000")
}

func TestRegister_DuplicatePanics(t *testing.T) {
	registerForTest(t, "fishsticks", func(_ string) (provider.Provider, error) {
		return &stubProvider{}, nil
	})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected duplicate Register to panic")
		}
	}()
	provider.Register("fishsticks", func(_ string) (provider.Provider, error) {
		return &stubProvider{}, nil
	})
}

func TestResolveSpec_FirstNonEmptyWins(t *testing.T) {
	cases := []struct {
		name       string
		candidates []string
		want       string
	}{
		{"all empty falls back to default", []string{"", "", ""}, provider.DefaultModelSpec},
		{"no candidates falls back to default", nil, provider.DefaultModelSpec},
		{"first non-empty wins", []string{"openai:gpt-4o", "claude:sonnet-4-5"}, "openai:gpt-4o"},
		{"skips leading empties", []string{"", "", "claude:opus-4-7"}, "claude:opus-4-7"},
		{"single non-empty", []string{"yeti:abominable-9000"}, "yeti:abominable-9000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := provider.ResolveSpec(tc.candidates...); got != tc.want {
				t.Errorf("ResolveSpec(%v) = %q, want %q", tc.candidates, got, tc.want)
			}
		})
	}
}

func TestRegisteredPrefixes_Sorted(t *testing.T) {
	prefixes := provider.RegisteredPrefixes()
	for i := 1; i < len(prefixes); i++ {
		if prefixes[i-1] > prefixes[i] {
			t.Errorf("RegisteredPrefixes not sorted: %v", prefixes)
			break
		}
	}
}
