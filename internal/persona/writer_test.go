package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/VoterBloc/gollm-qa/internal/agent"
)

func bigfootIdentity() GeneratedIdentity {
	return GeneratedIdentity{
		FirstName:   "Bartholomew",
		LastName:    "Sasquatch",
		Age:         47,
		State:       "WA",
		Occupation:  "Cryptozoologist",
		Email:       "bart.sasquatch@gollm-test.example",
		Username:    "bart_sasquatch",
		Password:    "B!gfootRules77",
		Description: "Hobbyist cryptozoologist with a podcast.",
		Behavior:    "engaged",
		Interests:   []string{"bigfoot", "trail cams"},
		Goals:       []string{"join a sightings bloc", "post a footprint cast"},
	}
}

func TestWrite_HappyPath(t *testing.T) {
	dir := t.TempDir()

	tpl := map[string]string{
		"email":     "{{email}}",
		"username":  "{{username}}",
		"password":  "{{password}}",
		"firstName": "{{firstName}}",
		"lastName":  "{{lastName}}",
	}

	path, err := Write(bigfootIdentity(), WriteOptions{
		OutputDir:        dir,
		CohortName:       "bigfoot-believers",
		RegisterTemplate: tpl,
	})
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if !strings.HasSuffix(path, "bigfoot-believers-bartholomew-sasquatch.yaml") {
		t.Errorf("unexpected filename: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}

	var p agent.Persona
	if err := yaml.Unmarshal(data, &p); err != nil {
		t.Fatalf("unmarshaling written persona: %v", err)
	}

	if p.Name != "Bartholomew Sasquatch" {
		t.Errorf("unexpected name: %q", p.Name)
	}
	if p.Behavior != agent.BehaviorEngaged {
		t.Errorf("unexpected behavior: %q", p.Behavior)
	}
	if p.Credentials.Identifier != "bart.sasquatch@gollm-test.example" {
		t.Errorf("unexpected credentials identifier: %q", p.Credentials.Identifier)
	}
	if p.Credentials.Password != "B!gfootRules77" {
		t.Errorf("unexpected credentials password: %q", p.Credentials.Password)
	}
	if p.RegisterInput["email"] != "bart.sasquatch@gollm-test.example" {
		t.Errorf("expected email substituted, got %v", p.RegisterInput["email"])
	}
	if p.RegisterInput["firstName"] != "Bartholomew" {
		t.Errorf("expected firstName substituted, got %v", p.RegisterInput["firstName"])
	}
	if p.Tags["state"] != "WA" {
		t.Errorf("expected state tag WA, got %q", p.Tags["state"])
	}
	if p.Tags["age"] != "47" {
		t.Errorf("expected age tag 47, got %q", p.Tags["age"])
	}
	if !strings.Contains(p.Tags["interests"], "bigfoot") {
		t.Errorf("expected interests to contain bigfoot, got %q", p.Tags["interests"])
	}
}

func TestWrite_StampsCohortAndCampaignTags(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(bigfootIdentity(), WriteOptions{
		OutputDir:    dir,
		CohortName:   "bigfoot-believers",
		CampaignName: "staging-leaders-2026-05.yaml",
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, _ := os.ReadFile(path)
	var p agent.Persona
	if err := yaml.Unmarshal(data, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Tags["cohort"] != "bigfoot-believers" {
		t.Errorf("tags.cohort = %q, want %q", p.Tags["cohort"], "bigfoot-believers")
	}
	if p.Tags["campaign"] != "staging-leaders-2026-05.yaml" {
		t.Errorf("tags.campaign = %q, want %q", p.Tags["campaign"], "staging-leaders-2026-05.yaml")
	}
}

func TestWrite_OmitsCohortTagsWhenUnset(t *testing.T) {
	// CLI users running `gollm run --personas <dir>` without a
	// generation context don't have cohort lineage to claim — empty
	// values shouldn't end up as empty-string tags.
	dir := t.TempDir()
	path, err := Write(bigfootIdentity(), WriteOptions{
		OutputDir:  dir,
		CohortName: "",
		// CampaignName intentionally unset
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, _ := os.ReadFile(path)
	var p agent.Persona
	yaml.Unmarshal(data, &p)
	if _, ok := p.Tags["cohort"]; ok {
		t.Errorf("expected no cohort tag when CohortName empty, got %q", p.Tags["cohort"])
	}
	if _, ok := p.Tags["campaign"]; ok {
		t.Errorf("expected no campaign tag when CampaignName empty, got %q", p.Tags["campaign"])
	}
}

func TestWrite_CohortNameTaggedEvenWithoutCampaign(t *testing.T) {
	// Partial context — some test harnesses might supply a cohort
	// but not a campaign file (e.g. ad-hoc generation outside the
	// seed command). Tag what we have.
	dir := t.TempDir()
	path, err := Write(bigfootIdentity(), WriteOptions{
		OutputDir:  dir,
		CohortName: "loners",
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, _ := os.ReadFile(path)
	var p agent.Persona
	yaml.Unmarshal(data, &p)
	if p.Tags["cohort"] != "loners" {
		t.Errorf("tags.cohort = %q, want %q", p.Tags["cohort"], "loners")
	}
	if _, ok := p.Tags["campaign"]; ok {
		t.Errorf("expected no campaign tag, got %q", p.Tags["campaign"])
	}
}

func TestWrite_NoTemplate_OmitsRegisterInput(t *testing.T) {
	dir := t.TempDir()

	path, err := Write(bigfootIdentity(), WriteOptions{
		OutputDir:  dir,
		CohortName: "no-template",
	})
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	var p agent.Persona
	yaml.Unmarshal(data, &p)
	if len(p.RegisterInput) != 0 {
		t.Errorf("expected no register_input without template, got %v", p.RegisterInput)
	}
}

func TestWrite_FilenameCollisionGetsSuffix(t *testing.T) {
	dir := t.TempDir()
	id := bigfootIdentity()

	first, err := Write(id, WriteOptions{OutputDir: dir, CohortName: "x"})
	if err != nil {
		t.Fatalf("first Write() error: %v", err)
	}
	second, err := Write(id, WriteOptions{OutputDir: dir, CohortName: "x"})
	if err != nil {
		t.Fatalf("second Write() error: %v", err)
	}
	if first == second {
		t.Fatal("expected second file to have a unique path")
	}
	if !strings.HasSuffix(second, "-2.yaml") {
		t.Errorf("expected -2 suffix on collision, got %s", second)
	}
}

func TestWrite_CreatesMissingDir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "deeply", "nested", "out")

	if _, err := Write(bigfootIdentity(), WriteOptions{OutputDir: target, CohortName: "creators"}); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("expected output dir to be created, got: %v", err)
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Bartholomew Sasquatch", "bartholomew-sasquatch"},
		{"Mavis O'Mothman", "mavis-o-mothman"},
		{"  Yeti  Snowfoot  ", "yeti-snowfoot"},
		{"Dr. P. Q. von Cryptid III", "dr-p-q-von-cryptid-iii"},
		{"!!! All Symbols !!!", "all-symbols"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := slugify(tt.in); got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRenderTemplate_MultipleSubstitutions(t *testing.T) {
	id := GeneratedIdentity{
		FirstName: "Mavis",
		LastName:  "Mothman",
		Email:     "mavis@example.test",
		Username:  "mavis_m",
		Password:  "P0intPleas@nt!",
		State:     "WV",
	}
	tpl := map[string]string{
		"display":    "{{firstName}} {{lastName}}",
		"email_copy": "{{email}}",
		"raw":        "no placeholders here",
	}
	out := renderTemplate(tpl, id)
	if out["display"] != "Mavis Mothman" {
		t.Errorf("unexpected display: %v", out["display"])
	}
	if out["email_copy"] != "mavis@example.test" {
		t.Errorf("unexpected email_copy: %v", out["email_copy"])
	}
	if out["raw"] != "no placeholders here" {
		t.Errorf("expected raw passthrough, got: %v", out["raw"])
	}
}
