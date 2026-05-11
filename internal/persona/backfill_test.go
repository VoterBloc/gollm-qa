package persona

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/VoterBloc/gollm-qa/internal/agent"
)

// silentLogger discards backfill log lines so test output stays clean.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeCampaign(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePersonaYAML(t *testing.T, dir, name string, p agent.Persona) {
	t.Helper()
	data, err := yaml.Marshal(&p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readPersonaYAML(t *testing.T, path string) agent.Persona {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p agent.Persona
	if err := yaml.Unmarshal(data, &p); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return p
}

func TestBackfillTags_TagsCohortPrefixedFiles(t *testing.T) {
	personasDir := t.TempDir()
	campaignsDir := t.TempDir()

	// Campaign with a "Bigfoot Believers" cohort. slugify produces
	// "bigfoot-believers", which is what the seed writer uses as
	// the filename prefix.
	writeCampaign(t, campaignsDir, "cryptid-leaders.yaml", `
brief_global: Cryptid hunters across the US.
cohorts:
  - name: "Bigfoot Believers"
    count: 1
    brief: Pacific Northwest cryptid hunters.
`)

	// Persona whose filename has the cohort prefix. Note no
	// tags.cohort yet — backfill should add it.
	writePersonaYAML(t, personasDir, "bigfoot-believers-bartholomew-sasquatch.yaml", agent.Persona{
		Name:        "Bartholomew Sasquatch",
		Description: "Hobbyist cryptozoologist.",
		Goals:       []string{"find tracks"},
		Behavior:    agent.BehaviorEngaged,
		Tags:        map[string]string{"state": "WA"},
	})

	report, err := BackfillTags(personasDir, campaignsDir, false, silentLogger())
	if err != nil {
		t.Fatalf("BackfillTags: %v", err)
	}

	if len(report.Tagged) != 1 {
		t.Fatalf("expected 1 tagged file, got %d (report=%+v)", len(report.Tagged), report)
	}
	tagged := readPersonaYAML(t, report.Tagged[0])
	if tagged.Tags["cohort"] != "Bigfoot Believers" {
		t.Errorf("cohort = %q, want %q", tagged.Tags["cohort"], "Bigfoot Believers")
	}
	if tagged.Tags["campaign"] != "cryptid-leaders.yaml" {
		t.Errorf("campaign = %q, want %q", tagged.Tags["campaign"], "cryptid-leaders.yaml")
	}
	// Pre-existing tags should be preserved.
	if tagged.Tags["state"] != "WA" {
		t.Errorf("pre-existing tag lost: state = %q, want WA", tagged.Tags["state"])
	}
}

func TestBackfillTags_SkipsAlreadyTagged(t *testing.T) {
	personasDir := t.TempDir()
	campaignsDir := t.TempDir()
	writeCampaign(t, campaignsDir, "leaders.yaml", `cohorts:
  - name: "Yetis"
    count: 1
    brief: cold cryptids
`)
	writePersonaYAML(t, personasDir, "yetis-greta-snowfoot.yaml", agent.Persona{
		Name:     "Greta Snowfoot",
		Behavior: agent.BehaviorLurker,
		Tags:     map[string]string{"cohort": "DO NOT OVERWRITE", "state": "AK"},
	})

	report, err := BackfillTags(personasDir, campaignsDir, false, silentLogger())
	if err != nil {
		t.Fatalf("BackfillTags: %v", err)
	}
	if len(report.Tagged) != 0 {
		t.Errorf("expected 0 tagged (already-tagged should skip), got %d", len(report.Tagged))
	}
	if len(report.AlreadyTagged) != 1 {
		t.Errorf("expected 1 already-tagged, got %d", len(report.AlreadyTagged))
	}
	// And the existing cohort tag must remain untouched.
	p := readPersonaYAML(t, filepath.Join(personasDir, "yetis-greta-snowfoot.yaml"))
	if p.Tags["cohort"] != "DO NOT OVERWRITE" {
		t.Errorf("existing cohort tag overwritten: %q", p.Tags["cohort"])
	}
}

func TestBackfillTags_SkipsFilesWithoutCohortPrefix(t *testing.T) {
	// Mirrors the four loose personas currently checked in
	// (jake-morrison.yaml, etc.) — predates the cohort concept;
	// shouldn't get false attributions.
	personasDir := t.TempDir()
	campaignsDir := t.TempDir()
	writeCampaign(t, campaignsDir, "leaders.yaml", `cohorts:
  - name: "Yetis"
    count: 1
    brief: cold cryptids
`)
	writePersonaYAML(t, personasDir, "jake-morrison.yaml", agent.Persona{
		Name:     "Jake Morrison",
		Behavior: agent.BehaviorLurker,
	})

	report, err := BackfillTags(personasDir, campaignsDir, false, silentLogger())
	if err != nil {
		t.Fatalf("BackfillTags: %v", err)
	}
	if len(report.Tagged) != 0 {
		t.Errorf("expected 0 tagged, got %d", len(report.Tagged))
	}
	if len(report.NoCohortPrefix) != 1 {
		t.Errorf("expected 1 no-prefix, got %d", len(report.NoCohortPrefix))
	}
	// And the file's tags are unchanged (no cohort/campaign added).
	p := readPersonaYAML(t, filepath.Join(personasDir, "jake-morrison.yaml"))
	if _, ok := p.Tags["cohort"]; ok {
		t.Errorf("unexpected cohort tag added: %q", p.Tags["cohort"])
	}
}

func TestBackfillTags_SkipsUnmatchedCohort(t *testing.T) {
	// File has a cohort-shaped prefix but the cohort isn't declared
	// in any campaign — better untagged than guess.
	personasDir := t.TempDir()
	campaignsDir := t.TempDir()
	writeCampaign(t, campaignsDir, "leaders.yaml", `cohorts:
  - name: "Yetis"
    count: 1
    brief: cold cryptids
`)
	writePersonaYAML(t, personasDir, "mothmen-mavis-mothman.yaml", agent.Persona{
		Name:     "Mavis Mothman",
		Behavior: agent.BehaviorEngaged,
	})

	report, err := BackfillTags(personasDir, campaignsDir, false, silentLogger())
	if err != nil {
		t.Fatalf("BackfillTags: %v", err)
	}
	if len(report.UnmatchedCohort) != 1 {
		t.Errorf("expected 1 unmatched-cohort, got %d", len(report.UnmatchedCohort))
	}
}

func TestBackfillTags_SkipsAmbiguousCohort(t *testing.T) {
	// Two campaigns both declare the same cohort name. Attribution
	// is ambiguous; the safe move is to leave the file untagged
	// rather than pick a winner.
	personasDir := t.TempDir()
	campaignsDir := t.TempDir()
	writeCampaign(t, campaignsDir, "a.yaml", `cohorts:
  - name: "Cryptids"
    count: 1
    brief: same name in two campaigns
`)
	writeCampaign(t, campaignsDir, "b.yaml", `cohorts:
  - name: "Cryptids"
    count: 1
    brief: ditto, ambiguous attribution
`)
	writePersonaYAML(t, personasDir, "cryptids-charlie-chimera.yaml", agent.Persona{
		Name:     "Charlie Chimera",
		Behavior: agent.BehaviorEngaged,
	})

	report, err := BackfillTags(personasDir, campaignsDir, false, silentLogger())
	if err != nil {
		t.Fatalf("BackfillTags: %v", err)
	}
	if len(report.AmbiguousCohort) != 1 {
		t.Errorf("expected 1 ambiguous-cohort, got %d", len(report.AmbiguousCohort))
	}
	if len(report.Tagged) != 0 {
		t.Errorf("expected 0 tagged (ambiguous should skip), got %d", len(report.Tagged))
	}
}

func TestBackfillTags_DryRunMakesNoChanges(t *testing.T) {
	personasDir := t.TempDir()
	campaignsDir := t.TempDir()
	writeCampaign(t, campaignsDir, "leaders.yaml", `cohorts:
  - name: "Bigfoot Believers"
    count: 1
    brief: PNW
`)
	personaPath := filepath.Join(personasDir, "bigfoot-believers-bartholomew-sasquatch.yaml")
	writePersonaYAML(t, personasDir, "bigfoot-believers-bartholomew-sasquatch.yaml", agent.Persona{
		Name:     "Bartholomew Sasquatch",
		Behavior: agent.BehaviorEngaged,
	})
	before, err := os.ReadFile(personaPath)
	if err != nil {
		t.Fatal(err)
	}

	report, err := BackfillTags(personasDir, campaignsDir, true, silentLogger())
	if err != nil {
		t.Fatalf("BackfillTags: %v", err)
	}
	if len(report.Tagged) != 1 {
		t.Errorf("dry-run should still report would-be-tagged (got %d)", len(report.Tagged))
	}

	after, err := os.ReadFile(personaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(string(before), string(after)) {
		t.Errorf("dry-run modified the file. before:\n%s\nafter:\n%s", before, after)
	}
}

func TestBackfillTags_RecursesIntoCohortSubdirs(t *testing.T) {
	// Layout: personas/sasquatch/sasquatch-bartholomew-sasquatch.yaml
	// covers the case where seed wrote into a per-cohort subdirectory.
	personasDir := t.TempDir()
	campaignsDir := t.TempDir()
	writeCampaign(t, campaignsDir, "leaders.yaml", `cohorts:
  - name: "Sasquatch"
    count: 1
    brief: tall
`)
	subDir := filepath.Join(personasDir, "sasquatch")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePersonaYAML(t, subDir, "sasquatch-bartholomew-sasquatch.yaml", agent.Persona{
		Name:     "Bartholomew Sasquatch",
		Behavior: agent.BehaviorEngaged,
	})

	report, err := BackfillTags(personasDir, campaignsDir, false, silentLogger())
	if err != nil {
		t.Fatalf("BackfillTags: %v", err)
	}
	if len(report.Tagged) != 1 {
		t.Fatalf("expected 1 tagged in subdir, got %d (report=%+v)", len(report.Tagged), report)
	}
	p := readPersonaYAML(t, report.Tagged[0])
	if p.Tags["cohort"] != "Sasquatch" {
		t.Errorf("cohort = %q, want Sasquatch", p.Tags["cohort"])
	}
}
