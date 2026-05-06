// Package persona handles LLM-driven synthesis and YAML serialization of
// new persona definitions. Used by `gollm seed` to bulk-generate test
// populations from natural-language briefs.
package persona

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/VoterBloc/gollm-qa/internal/provider"
)

// GeneratedIdentity is the normalized output of the LLM persona generator.
// Per-app register_input shape is layered on later by the writer using the
// app config's persona_register_template.
type GeneratedIdentity struct {
	FirstName   string   `json:"firstName"`
	LastName    string   `json:"lastName"`
	Age         int      `json:"age"`
	State       string   `json:"state"`
	Occupation  string   `json:"occupation"`
	Email       string   `json:"email"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	Description string   `json:"description"`
	Behavior    string   `json:"behavior"`
	Interests   []string `json:"interests"`
	Goals       []string `json:"goals"`
}

// FullName returns a display name for filenames and logs.
func (g *GeneratedIdentity) FullName() string {
	return strings.TrimSpace(g.FirstName + " " + g.LastName)
}

// Generator wraps an LLM provider with seed-specific prompting.
type Generator struct {
	provider provider.Provider
}

// NewGenerator returns a generator that uses the given LLM provider for
// persona synthesis.
func NewGenerator(p provider.Provider) *Generator {
	return &Generator{provider: p}
}

// Generate calls the LLM to produce `count` personas matching the brief.
// briefGlobal is optional cross-cohort context. The returned slice may be
// shorter than count if the model returns fewer rows; callers should check.
func (g *Generator) Generate(ctx context.Context, brief, briefGlobal string, count int) ([]GeneratedIdentity, error) {
	if count <= 0 {
		return nil, fmt.Errorf("generate: count must be > 0")
	}
	if strings.TrimSpace(brief) == "" {
		return nil, fmt.Errorf("generate: brief cannot be empty")
	}

	system := buildSystemPrompt(briefGlobal)
	user := buildUserPrompt(brief, count)

	resp, err := g.provider.Chat(ctx,
		[]provider.Message{
			{Role: provider.RoleSystem, Content: system},
			{Role: provider.RoleUser, Content: user},
		},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("generate: provider call failed: %w", err)
	}

	identities, err := parseResponse(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("generate: parsing response: %w (raw: %s)", err, truncate(resp.Message.Content, 400))
	}

	for i := range identities {
		if err := validateIdentity(&identities[i]); err != nil {
			return nil, fmt.Errorf("generate: persona %d: %w", i, err)
		}
	}
	return identities, nil
}

func buildSystemPrompt(briefGlobal string) string {
	var b strings.Builder
	b.WriteString("You generate synthetic user personas for a scale-test platform. ")
	b.WriteString("Your output is consumed by software, so format strictly. ")
	b.WriteString("Personas in a single batch must be genuinely distinct from one another — ")
	b.WriteString("vary age within plausible ranges, exact occupation, region within the segment, ")
	b.WriteString("voice, and tech literacy. Avoid stereotypes. Avoid making every persona sound ")
	b.WriteString("like a polished press release; some are casual, some terse, some chatty.\n")
	if briefGlobal != "" {
		b.WriteString("\nGlobal context (applies to every persona):\n")
		b.WriteString(briefGlobal)
		b.WriteString("\n")
	}
	return b.String()
}

func buildUserPrompt(brief string, count int) string {
	return fmt.Sprintf(`Generate %d distinct personas matching this population segment:

%s

For each persona, return these fields:
- firstName (no honorific)
- lastName
- age (specific number)
- state (two-letter US code, uppercase)
- occupation (job title or honest descriptor like "retired teacher")
- email (use a clearly fake test domain like @gollm-test.example)
- username (lowercase, dots or underscores, no spaces)
- password (10+ chars, mixed case, at least one digit and one symbol)
- description (2-3 sentences in third person establishing voice, background, why they're on the platform)
- behavior (one of: "engaged", "moderate", "lurker" — vary across the cohort)
- interests (array of 2-4 specific topics; not generic categories)
- goals (array of 2-4 specific actions this persona wants to take on the platform)

Return ONLY a JSON object of the form:
{"personas": [ { ... }, { ... } ]}

No prose, no markdown fences, no commentary. Just the JSON object.`, count, brief)
}

// parseResponse extracts the JSON object from a model response that may have
// stray prose or markdown fences around it, then unmarshals to identities.
func parseResponse(content string) ([]GeneratedIdentity, error) {
	jsonStr := extractJSONObject(content)
	if jsonStr == "" {
		return nil, fmt.Errorf("no JSON object found in response")
	}

	var wrapper struct {
		Personas []GeneratedIdentity `json:"personas"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &wrapper); err != nil {
		return nil, fmt.Errorf("unmarshaling personas: %w", err)
	}
	if len(wrapper.Personas) == 0 {
		return nil, fmt.Errorf("response contained zero personas")
	}
	return wrapper.Personas, nil
}

// extractJSONObject returns the substring from the first { to the matching
// closing } (tracking nesting). Tolerates prose / markdown fences before
// and after the object.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inString {
			escape = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func validateIdentity(g *GeneratedIdentity) error {
	if g.FirstName == "" || g.LastName == "" {
		return fmt.Errorf("missing firstName or lastName")
	}
	if g.Email == "" {
		return fmt.Errorf("missing email")
	}
	if g.Username == "" {
		return fmt.Errorf("missing username")
	}
	if g.Password == "" {
		return fmt.Errorf("missing password")
	}
	if g.Description == "" {
		return fmt.Errorf("missing description")
	}
	switch strings.ToLower(g.Behavior) {
	case "engaged", "moderate", "lurker":
		g.Behavior = strings.ToLower(g.Behavior)
	default:
		return fmt.Errorf("invalid behavior %q (expected engaged|moderate|lurker)", g.Behavior)
	}
	if len(g.Goals) == 0 {
		return fmt.Errorf("missing goals")
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
