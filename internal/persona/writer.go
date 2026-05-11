package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/VoterBloc/gollm-qa/internal/agent"
)

// WriteOptions controls persona file output.
type WriteOptions struct {
	// OutputDir is the directory to write persona YAMLs into. Created if missing.
	OutputDir string
	// CohortName prefixes the generated filename for traceability and is
	// stamped on the persona's tags.cohort so cohort lineage survives the
	// generate → persist → run lifecycle (#56).
	CohortName string
	// CampaignName is the basename of the campaign YAML that produced this
	// persona (e.g. "staging-leaders-2026-05.yaml"). Stamped on tags.campaign
	// for the same lineage reason. Empty when the caller doesn't have a
	// campaign context.
	CampaignName string
	// RegisterTemplate maps register_input keys to templates referencing
	// {{firstName}}, {{lastName}}, {{email}}, {{username}}, {{password}}.
	// If empty, the persona's register_input is omitted.
	RegisterTemplate map[string]string
}

// Write renders a GeneratedIdentity to a persona YAML file in opts.OutputDir.
// Returns the path of the written file. Filenames collide if two personas
// slug to the same value; collisions get a numeric suffix.
func Write(id GeneratedIdentity, opts WriteOptions) (string, error) {
	if opts.OutputDir == "" {
		return "", fmt.Errorf("write: OutputDir is required")
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return "", fmt.Errorf("creating output dir: %w", err)
	}

	persona := buildPersona(id, opts.RegisterTemplate)
	stampCohortTags(persona, opts.CohortName, opts.CampaignName)

	data, err := yaml.Marshal(persona)
	if err != nil {
		return "", fmt.Errorf("marshaling persona YAML: %w", err)
	}

	path, err := uniquePath(opts.OutputDir, filename(opts.CohortName, id))
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("writing persona file: %w", err)
	}
	return path, nil
}

func buildPersona(id GeneratedIdentity, tpl map[string]string) *agent.Persona {
	p := &agent.Persona{
		Name:        id.FullName(),
		Description: id.Description,
		Goals:       id.Goals,
		Behavior:    agent.Behavior(strings.ToLower(id.Behavior)),
		Tags: map[string]string{
			"state":      id.State,
			"age":        fmt.Sprintf("%d", id.Age),
			"occupation": id.Occupation,
		},
		Credentials: agent.Credentials{
			Identifier: id.Email,
			Password:   id.Password,
		},
	}
	if len(id.Interests) > 0 {
		p.Tags["interests"] = strings.Join(id.Interests, ", ")
	}
	if len(tpl) > 0 {
		p.RegisterInput = renderTemplate(tpl, id)
	}
	return p
}

func renderTemplate(tpl map[string]string, id GeneratedIdentity) map[string]any {
	subs := map[string]string{
		"{{firstName}}": id.FirstName,
		"{{lastName}}":  id.LastName,
		"{{email}}":     id.Email,
		"{{username}}":  id.Username,
		"{{password}}":  id.Password,
		"{{state}}":     id.State,
	}
	out := make(map[string]any, len(tpl))
	for k, v := range tpl {
		rendered := v
		for placeholder, value := range subs {
			rendered = strings.ReplaceAll(rendered, placeholder, value)
		}
		out[k] = rendered
	}
	return out
}

// stampCohortTags writes cohort + campaign attribution onto the
// persona's tags map so cohort identity survives past filename-only
// prefix conventions. Empty values are skipped — a CLI user running
// `gollm run --personas <dir>` without a generation context doesn't
// have cohort lineage to claim, and tagging "" would just be noise.
//
// Free-form tags map matches the existing convention (see
// internal/persona/writer.go's buildPersona) — no schema enforced.
func stampCohortTags(p *agent.Persona, cohortName, campaignName string) {
	if p.Tags == nil {
		p.Tags = map[string]string{}
	}
	if cohortName != "" {
		p.Tags["cohort"] = cohortName
	}
	if campaignName != "" {
		p.Tags["campaign"] = campaignName
	}
}

// filename returns a slug-ified filename like "bigfoot-believers-bart-sasquatch.yaml".
func filename(cohortName string, id GeneratedIdentity) string {
	slug := slugify(id.FullName())
	if slug == "" {
		slug = "persona"
	}
	if cohortName != "" {
		return fmt.Sprintf("%s-%s.yaml", slugify(cohortName), slug)
	}
	return slug + ".yaml"
}

// uniquePath returns dir/name, adding -2/-3/etc. if the file already exists.
// Errors after 1000 collisions rather than overwriting — that case shouldn't
// happen in normal use, and silent overwrite would lose work.
func uniquePath(dir, name string) (string, error) {
	candidate := filepath.Join(dir, name)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate, nil
	}
	base := strings.TrimSuffix(name, ".yaml")
	for i := 2; i < 1000; i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d.yaml", base, i))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find unique filename for %q after 1000 attempts", name)
}

// slugify converts a free-form string to a filesystem-safe slug. Lowercases,
// replaces non-alphanumerics with single dashes, trims leading/trailing dashes.
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
