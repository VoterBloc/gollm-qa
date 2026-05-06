package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// CampaignConfig describes a batch persona-generation run. A campaign is a
// list of cohorts (segments of the user population), each with its own brief.
// Run by `gollm seed --campaign <path>`.
type CampaignConfig struct {
	// BriefGlobal is optional context prepended to every cohort's prompt.
	// Use it to communicate cross-cutting concerns ("VoterBloc users in the
	// 2026 election cycle, focused on state-level civic engagement").
	BriefGlobal string `yaml:"brief_global"`
	// Cohorts is the list of population segments to generate.
	Cohorts []CohortConfig `yaml:"cohorts"`
}

// CohortConfig is one segment of the synthetic user population.
type CohortConfig struct {
	// Name identifies the cohort and prefixes generated persona filenames.
	// Should be a slug like "retired-engaged" or "college-active".
	Name string `yaml:"name"`
	// Count is how many personas to generate for this cohort.
	Count int `yaml:"count"`
	// Brief is the natural-language description of who's in this cohort.
	// More specific produces more differentiated personas — aim for a
	// population segment with constrained demographics, interests, and
	// posture, not a single fully-specified individual.
	Brief string `yaml:"brief"`
}

// LoadCampaign reads and parses a campaign YAML file.
func LoadCampaign(path string) (*CampaignConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading campaign config: %w", err)
	}

	var cfg CampaignConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing campaign config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (cfg *CampaignConfig) validate() error {
	if len(cfg.Cohorts) == 0 {
		return fmt.Errorf("campaign config: at least one cohort is required")
	}
	seen := make(map[string]bool)
	for i, c := range cfg.Cohorts {
		if c.Name == "" {
			return fmt.Errorf("campaign config: cohorts[%d].name is required", i)
		}
		if seen[c.Name] {
			return fmt.Errorf("campaign config: duplicate cohort name %q", c.Name)
		}
		seen[c.Name] = true
		if c.Count <= 0 {
			return fmt.Errorf("campaign config: cohorts[%d] (%s) count must be > 0", i, c.Name)
		}
		if c.Brief == "" {
			return fmt.Errorf("campaign config: cohorts[%d] (%s) brief is required", i, c.Name)
		}
	}
	return nil
}

// TotalPersonas returns the sum of counts across all cohorts.
func (cfg *CampaignConfig) TotalPersonas() int {
	n := 0
	for _, c := range cfg.Cohorts {
		n += c.Count
	}
	return n
}
