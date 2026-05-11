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
//
// A single malformed file lands in MalformedYAML and the walk
// continues — better to tag the other ~30 personas in the tree than
// abort because one was hand-edited into nonsense.
type BackfillReport struct {
	Examined        int
	Tagged          []string
	AlreadyTagged   []string
	NoCohortPrefix  []string
	UnmatchedCohort []string
	AmbiguousCohort []string
	MalformedYAML   []string
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
//
// Per-file errors (read failure, parse failure) land in
// MalformedYAML and return nil so the walk continues. Write errors
// (tmp file, rename) propagate up because they indicate a system
// problem the operator needs to address before retrying.
func processPersonaFile(path, fname string, index map[string][]cohortIndexEntry, report *BackfillReport, dryRun bool, logger *slog.Logger) error {
	report.Examined++

	data, err := os.ReadFile(path)
	if err != nil {
		report.MalformedYAML = append(report.MalformedYAML, path)
		logger.Warn("backfill: skipping unreadable file", "file", path, "error", err)
		return nil
	}
	var p agent.Persona
	if err := yaml.Unmarshal(data, &p); err != nil {
		report.MalformedYAML = append(report.MalformedYAML, path)
		logger.Warn("backfill: skipping unparseable YAML", "file", path, "error", err)
		return nil
	}

	// Idempotence: only the cohort tag is consulted. If a file
	// somehow has campaign set but not cohort (a partial
	// hand-edit), backfill proceeds and writes both — the cohort
	// tag is the canonical signal of "this persona is attributed"
	// and we'd rather complete the attribution than leave it half.
	if p.Tags["cohort"] != "" {
		report.AlreadyTagged = append(report.AlreadyTagged, path)
		return nil
	}

	// Personas without a parseable Name can't be matched (the
	// suffix-anchor doesn't exist). Defensive early return —
	// the stem == personaSlug branch below would technically
	// catch this when both are "", but failing explicitly is
	// clearer than relying on string-equality of empty strings.
	personaSlug := slugify(p.Name)
	if personaSlug == "" {
		report.NoCohortPrefix = append(report.NoCohortPrefix, path)
		return nil
	}

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

	if dryRun {
		logger.Info("backfill: would tag (dry-run)",
			"file", path, "cohort", entry.cohortName, "campaign", entry.campaignFile)
		report.Tagged = append(report.Tagged, path)
		return nil
	}

	// Use the yaml.Node API to modify tags in place rather than a
	// struct round-trip, which would silently drop any fields the
	// agent.Persona struct doesn't declare (notes, voting_history,
	// custom app-specific keys, etc.). Preserves hand-written
	// content the backfill never asked about.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// Defensive: same bytes parsed successfully into a struct
		// above, so a Node-parse failure here is unexpected.
		report.MalformedYAML = append(report.MalformedYAML, path)
		logger.Warn("backfill: node-unmarshal failed", "file", path, "error", err)
		return nil
	}
	if err := stampTagsInNode(&doc, entry.cohortName, entry.campaignFile); err != nil {
		return fmt.Errorf("stamping %s: %w", path, err)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	// Atomic write: stage to a sibling temp file, then rename.
	// Rename is atomic on POSIX (and on Windows since Go 1.5) so
	// an interrupted backfill (Ctrl-C, OOM, disk full mid-write)
	// can't leave a persona file truncated. The original stays
	// intact until rename swaps it in.
	tmpPath := path + ".bk-new"
	if err := os.WriteFile(tmpPath, out, 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, path, err)
	}

	logger.Info("backfill: tagged",
		"file", path, "cohort", entry.cohortName, "campaign", entry.campaignFile)
	report.Tagged = append(report.Tagged, path)
	return nil
}

// stampTagsInNode mutates the document's top-level mapping to add
// cohort + campaign entries under `tags:`. Operates on the
// yaml.Node tree rather than a Go struct so non-Persona fields in
// the source YAML (comments, custom keys, app-specific extensions)
// survive the round-trip.
//
// Shape assumptions: the document's first child is a mapping (every
// persona YAML we write has this shape). If `tags:` is absent, a
// fresh mapping is created. If `tags:` is present but isn't a
// mapping (e.g. someone wrote `tags: null` or `tags: []`), it gets
// replaced with a fresh mapping — better to land sensibly tagged
// than fail because of an upstream shape mistake.
func stampTagsInNode(doc *yaml.Node, cohort, campaign string) error {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("unexpected YAML structure (kind=%d, content=%d)", doc.Kind, len(doc.Content))
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return fmt.Errorf("top-level YAML must be a mapping (got kind=%d)", top.Kind)
	}
	tags := ensureMappingChild(top, "tags")
	setStringInMapping(tags, "cohort", cohort)
	setStringInMapping(tags, "campaign", campaign)
	return nil
}

// findValueInMapping returns the value node and its index in the
// mapping's Content slice, or (nil, -1) when the key isn't found.
// Mapping Content is laid out as key, value, key, value …
func findValueInMapping(m *yaml.Node, key string) (*yaml.Node, int) {
	if m.Kind != yaml.MappingNode {
		return nil, -1
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return m.Content[i+1], i + 1
		}
	}
	return nil, -1
}

// setStringInMapping inserts or updates a key with a string value
// under the given mapping. Resets Style/Content on overwrite so the
// marshaler picks a fresh default rather than carrying over whatever
// shape the prior value had.
func setStringInMapping(m *yaml.Node, key, value string) {
	if existing, idx := findValueInMapping(m, key); existing != nil {
		m.Content[idx].Kind = yaml.ScalarNode
		m.Content[idx].Tag = "!!str"
		m.Content[idx].Value = value
		m.Content[idx].Style = 0
		m.Content[idx].Content = nil
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

// ensureMappingChild returns the mapping node sitting under key on
// the parent mapping, creating one if absent. If the key exists but
// its value isn't a mapping (shape mistake — e.g. `tags: null`),
// the value gets coerced to an empty mapping in place rather than
// erroring; the backfill prioritizes "land sensibly" over "preserve
// user's broken shape."
func ensureMappingChild(parent *yaml.Node, key string) *yaml.Node {
	if existing, _ := findValueInMapping(parent, key); existing != nil {
		if existing.Kind == yaml.MappingNode {
			return existing
		}
		existing.Kind = yaml.MappingNode
		existing.Tag = "!!map"
		existing.Value = ""
		existing.Content = nil
		existing.Style = 0
		return existing
	}
	newMap := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		newMap,
	)
	return newMap
}
