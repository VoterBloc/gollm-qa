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

// validateIdentity rejects partial responses. Every field the user prompt
// asks for is checked; if the model degrades and omits something, we want a
// loud failure at seed time rather than a half-formed persona that breaks
// at registration time.
func validateIdentity(g *GeneratedIdentity) error {
	if g.FirstName == "" || g.LastName == "" {
		return fmt.Errorf("missing firstName or lastName")
	}
	if g.Age <= 0 {
		return fmt.Errorf("missing or invalid age")
	}
	if g.State == "" {
		return fmt.Errorf("missing state")
	}
	if g.Occupation == "" {
		return fmt.Errorf("missing occupation")
	}
	if g.Email == "" {
		return fmt.Errorf("missing email")
	}
	if g.Username == "" {
		return fmt.Errorf("missing username")
	}
	if err := validatePassword(g.Password); err != nil {
		return fmt.Errorf("password: %w", err)
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
	if len(g.Interests) == 0 {
		return fmt.Errorf("missing interests")
	}
	if len(g.Goals) == 0 {
		return fmt.Errorf("missing goals")
	}
	return nil
}

// validatePassword enforces the complexity the user prompt requests. Without
// this, a password like "asdf" would pass (non-empty) and only fail at
// registration time against the target app's password policy.
func validatePassword(p string) error {
	if len(p) < 10 {
		return fmt.Errorf("must be at least 10 characters")
	}
	var hasDigit, hasSymbol, hasUpper, hasLower bool
	for _, r := range p {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		default:
			hasSymbol = true
		}
	}
	if !hasDigit {
		return fmt.Errorf("must contain a digit")
	}
	if !hasSymbol {
		return fmt.Errorf("must contain a symbol")
	}
	if !hasUpper || !hasLower {
		return fmt.Errorf("must contain mixed case")
	}
	return nil
}

// SeenIdentities tracks email + username uniqueness across multiple cohorts
// in one seed run. Construct a single instance and pass it to each Dedupe
// call so a duplicate in cohort B is caught against cohort A's output.
type SeenIdentities struct {
	Emails    map[string]bool
	Usernames map[string]bool
}

// NewSeenIdentities returns an empty tracker.
func NewSeenIdentities() *SeenIdentities {
	return &SeenIdentities{
		Emails:    make(map[string]bool),
		Usernames: make(map[string]bool),
	}
}

// Dedupe rewrites email and username on identities that collide with anything
// already in `seen` (including each other). On collision it appends a numeric
// suffix to the local-part of the email and to the username; the modified
// identity is reported in `renamed` so callers can log a warning. The "two
// Bartholomews" failure mode is plausible from LLM output and would otherwise
// surface as a duplicate-key error at registration time, much later than
// necessary.
func Dedupe(identities []GeneratedIdentity, seen *SeenIdentities) (renamed []string) {
	for i := range identities {
		id := &identities[i]
		emailFix, usernameFix := false, false
		// Suffix off the original each time so collisions don't compound
		// (avoid `bart-2-3@…` when -2 also collides — try -3 from scratch).
		origEmail := id.Email
		for n := 2; seen.Emails[id.Email]; n++ {
			id.Email = suffixEmail(origEmail, n)
			emailFix = true
		}
		origUsername := id.Username
		for n := 2; seen.Usernames[id.Username]; n++ {
			id.Username = suffixUsername(origUsername, n)
			usernameFix = true
		}
		seen.Emails[id.Email] = true
		seen.Usernames[id.Username] = true
		if emailFix || usernameFix {
			renamed = append(renamed, id.FullName())
		}
	}
	return renamed
}

func suffixEmail(email string, n int) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return fmt.Sprintf("%s-%d", email, n)
	}
	return fmt.Sprintf("%s-%d%s", email[:at], n, email[at:])
}

func suffixUsername(username string, n int) string {
	return fmt.Sprintf("%s-%d", username, n)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
