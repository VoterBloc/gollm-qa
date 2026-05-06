package config

import (
	"strings"
	"testing"
)

const testCampaignYAML = `
brief_global: "Cryptid enthusiasts on a regional sightings platform"
cohorts:
  - name: bigfoot-believers
    count: 12
    brief: "Pacific Northwest forest dwellers, 40-65, deeply convinced of bigfoot's existence, varying PhotoShop literacy"
  - name: skeptical-debunkers
    count: 8
    brief: "Mid-career scientists or amateur skeptics, hostile to credulous reporting, very active commenters"
  - name: lurking-tourists
    count: 5
    brief: "Vacation hikers who saw something weird once, register but rarely post"
`

func TestLoadCampaign(t *testing.T) {
	path := writeTempFile(t, "campaign.yaml", testCampaignYAML)

	cfg, err := LoadCampaign(path)
	if err != nil {
		t.Fatalf("LoadCampaign() error: %v", err)
	}

	if cfg.BriefGlobal == "" {
		t.Error("expected brief_global to be populated")
	}
	if len(cfg.Cohorts) != 3 {
		t.Fatalf("expected 3 cohorts, got %d", len(cfg.Cohorts))
	}
	if cfg.Cohorts[0].Name != "bigfoot-believers" {
		t.Errorf("unexpected first cohort name: %q", cfg.Cohorts[0].Name)
	}
	if cfg.Cohorts[0].Count != 12 {
		t.Errorf("expected count 12, got %d", cfg.Cohorts[0].Count)
	}
	if cfg.TotalPersonas() != 25 {
		t.Errorf("expected total 25, got %d", cfg.TotalPersonas())
	}
}

func TestLoadCampaign_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		errText string
	}{
		{
			name:    "no cohorts",
			yaml:    `brief_global: "nothing here"`,
			errText: "at least one cohort is required",
		},
		{
			name: "missing name",
			yaml: `
cohorts:
  - count: 5
    brief: "Yetis"
`,
			errText: "cohorts[0].name is required",
		},
		{
			name: "zero count",
			yaml: `
cohorts:
  - name: empties
    count: 0
    brief: "Nobody"
`,
			errText: "count must be > 0",
		},
		{
			name: "missing brief",
			yaml: `
cohorts:
  - name: voiceless
    count: 3
`,
			errText: "brief is required",
		},
		{
			name: "duplicate name",
			yaml: `
cohorts:
  - name: nessie-fans
    count: 3
    brief: "Loch Ness fans"
  - name: nessie-fans
    count: 4
    brief: "More Loch Ness fans"
`,
			errText: "duplicate cohort name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, "bad.yaml", tt.yaml)
			_, err := LoadCampaign(path)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.errText) {
				t.Errorf("expected error containing %q, got: %v", tt.errText, err)
			}
		})
	}
}
