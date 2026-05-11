package persona

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/VoterBloc/gollm-qa/internal/agent"
	"github.com/VoterBloc/gollm-qa/internal/config"
)

// BackfillReport summarizes what BackfillTags did (or would do, in
// dry-run mode). Tagged is the list of files that received tags;
// the other slices record the reasons for skipping so an operator
// running the one-shot script can see exactly what got left alone.
type BackfillReport struct {
	Examined        int
	Tagged          []string
	AlreadyTagged   []string
	NoCohortPrefix  []string
	UnmatchedCohort []string
	AmbiguousCohort []string
}

// BackfillTags walks personasDir, identifies persona files whose
// filenames carry a `<cohort-slug>-<persona-slug>` prefix
// (the shape seed writes — see writer.go's filename()), matches
// the cohort slug back against the campaign YAMLs under
// campaignsDir, and stamps tags.cohort + tags.campaign on the
// persona's YAML in place.
//
// Idempotent — files where tags.cohort is already set are skipped.
// Files that don't look like they came from seed (no recognizable
// cohort prefix) are also skipped; better to leave them untagged
// than guess. The four loose personas in the repo's personas/ dir
// (jake-morrison.yaml etc.) fall into this skip-bucket — the
// expected outcome per issue #56.
//
// Recursive: handles both flat (personas/X.yaml) and nested
// (personas/<cohort>/X.yaml) layouts. dryRun reports without writing.
func BackfillTags(personasDir, campaignsDir string, dryRun bool, logger *slog.Logger) (*BackfillReport, error) {
	if logger == nil {
		logger = slog.Default()
	}
	index, err := indexCohorts(campaignsDir, logger)
	if err != nil {
		return nil, fmt.Errorf("indexing campaigns: %w", err)
	}

	report := &BackfillReport{}
	walkErr := filepath.WalkDir(personasDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		return processPersonaFile(path, d.Name(), index, report, dryRun, logger)
	})
	if walkErr != nil {
		return report, fmt.Errorf("walking personas dir: %w", walkErr)
	}
	return report, nil
}

// cohortIndexEntry captures where a cohort came from so we can stamp
// both the cohort name and the source campaign filename on each
// matching persona.
type cohortIndexEntry struct {
	cohortName   string
	campaignFile string
}

// indexCohorts walks campaignsDir and builds a map from
// slugify(cohort.Name) → all campaigns that declared that cohort.
// Two campaigns sharing the same cohort slug would render
// attribution ambiguous; the map's slice carries every match so the
// per-file logic can detect that and skip.
func indexCohorts(campaignsDir string, logger *slog.Logger) (map[string][]cohortIndexEntry, error) {
	index := make(map[string][]cohortIndexEntry)
	entries, err := os.ReadDir(campaignsDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(campaignsDir, e.Name())
		cfg, err := config.LoadCampaign(path)
		if err != nil {
			logger.Warn("backfill: skipping unparseable campaign", "file", e.Name(), "error", err)
			continue
		}
		for _, cohort := range cfg.Cohorts {
			slug := slugify(cohort.Name)
			if slug == "" {
				continue
			}
			index[slug] = append(index[slug], cohortIndexEntry{
				cohortName:   cohort.Name,
				campaignFile: e.Name(),
			})
		}
	}
	return index, nil
}

// processPersonaFile handles a single persona file: parse, check
// idempotence, recover cohort from the filename, look up against
// the index, and either write back or note the skip reason.
func processPersonaFile(path, fname string, index map[string][]cohortIndexEntry, report *BackfillReport, dryRun bool, logger *slog.Logger) error {
	report.Examined++

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var p agent.Persona
	if err := yaml.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	if p.Tags["cohort"] != "" {
		report.AlreadyTagged = append(report.AlreadyTagged, path)
		return nil
	}

	personaSlug := slugify(p.Name)
	stem := strings.TrimSuffix(fname, filepath.Ext(fname))

	// Filename must end in "-<personaSlug>" to claim cohort lineage.
	// stem == personaSlug means no prefix at all; HasSuffix check
	// guards against the persona's name not appearing where we
	// expect (e.g. files renamed by hand).
	if stem == personaSlug || !strings.HasSuffix(stem, "-"+personaSlug) {
		report.NoCohortPrefix = append(report.NoCohortPrefix, path)
		return nil
	}
	cohortSlug := strings.TrimSuffix(stem, "-"+personaSlug)

	entries, ok := index[cohortSlug]
	if !ok {
		report.UnmatchedCohort = append(report.UnmatchedCohort, path)
		logger.Info("backfill: cohort prefix matches no campaign cohort",
			"file", path, "cohort_slug", cohortSlug)
		return nil
	}
	if len(entries) > 1 {
		report.AmbiguousCohort = append(report.AmbiguousCohort, path)
		logger.Warn("backfill: cohort prefix matches multiple campaigns; leaving untagged",
			"file", path, "cohort_slug", cohortSlug, "matches", len(entries))
		return nil
	}
	entry := entries[0]

	if p.Tags == nil {
		p.Tags = map[string]string{}
	}
	p.Tags["cohort"] = entry.cohortName
	p.Tags["campaign"] = entry.campaignFile

	if dryRun {
		logger.Info("backfill: would tag (dry-run)",
			"file", path, "cohort", entry.cohortName, "campaign", entry.campaignFile)
		report.Tagged = append(report.Tagged, path)
		return nil
	}

	out, err := yaml.Marshal(&p)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	logger.Info("backfill: tagged",
		"file", path, "cohort", entry.cohortName, "campaign", entry.campaignFile)
	report.Tagged = append(report.Tagged, path)
	return nil
}
