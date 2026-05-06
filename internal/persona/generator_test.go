package persona

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/VoterBloc/gollm-qa/internal/provider"
)

// stubProvider returns a canned response for the next Chat call.
type stubProvider struct {
	response string
	err      error
	captured []provider.Message
}

func (s *stubProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool) (*provider.Response, error) {
	s.captured = msgs
	if s.err != nil {
		return nil, s.err
	}
	return &provider.Response{
		Message:    provider.Message{Role: provider.RoleAssistant, Content: s.response},
		StopReason: "end",
		Usage:      provider.Usage{InputTokens: 100, OutputTokens: 200},
	}, nil
}

func TestGenerator_HappyPath(t *testing.T) {
	stub := &stubProvider{response: `{"personas": [
		{
			"firstName": "Bartholomew",
			"lastName": "Sasquatch",
			"age": 47,
			"state": "WA",
			"occupation": "Cryptozoologist",
			"email": "bart.sasquatch@gollm-test.example",
			"username": "bart_sasquatch",
			"password": "B!gfootRules77",
			"description": "Bartholomew is a hobbyist cryptozoologist running a podcast about Pacific Northwest sightings.",
			"behavior": "engaged",
			"interests": ["bigfoot", "trail cams", "regional folklore"],
			"goals": ["join a Pacific Northwest sightings bloc", "post about a recent footprint cast"]
		},
		{
			"firstName": "Mavis",
			"lastName": "Mothman",
			"age": 62,
			"state": "WV",
			"occupation": "Retired librarian",
			"email": "mavis.mothman@gollm-test.example",
			"username": "mavis.m",
			"password": "P0intPleas@nt!",
			"description": "Mavis was at Point Pleasant in 1967. She does not want to talk about it but somehow always does.",
			"behavior": "moderate",
			"interests": ["mothman", "1960s folklore", "bridge engineering"],
			"goals": ["comment on Mothman threads", "find others from Point Pleasant"]
		}
	]}`}

	gen := NewGenerator(stub)
	identities, err := gen.Generate(context.Background(), "Cryptid believers, varied", "Regional sightings platform", 2)
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if len(identities) != 2 {
		t.Fatalf("expected 2 identities, got %d", len(identities))
	}
	if identities[0].FirstName != "Bartholomew" {
		t.Errorf("unexpected firstName: %q", identities[0].FirstName)
	}
	if identities[0].FullName() != "Bartholomew Sasquatch" {
		t.Errorf("unexpected full name: %q", identities[0].FullName())
	}

	// Verify the brief and global context made it into the prompt.
	var allContent strings.Builder
	for _, m := range stub.captured {
		allContent.WriteString(m.Content)
	}
	prompt := allContent.String()
	if !strings.Contains(prompt, "Cryptid believers, varied") {
		t.Errorf("expected brief in prompt")
	}
	if !strings.Contains(prompt, "Regional sightings platform") {
		t.Errorf("expected brief_global in prompt")
	}
}

func TestGenerator_StripsCodeFences(t *testing.T) {
	stub := &stubProvider{response: "Here are your personas:\n\n```json\n" + `{"personas": [
		{
			"firstName": "Yeti",
			"lastName": "Snowfoot",
			"age": 200,
			"state": "AK",
			"occupation": "Mountaineer",
			"email": "yeti@gollm-test.example",
			"username": "yeti_snow",
			"password": "Abom!nable22",
			"description": "Tall, hirsute, prone to long silences.",
			"behavior": "lurker",
			"interests": ["snowfields", "solitude"],
			"goals": ["observe without being seen"]
		}
	]}` + "\n```\n\nHope this helps!"}

	gen := NewGenerator(stub)
	identities, err := gen.Generate(context.Background(), "Snowy creatures", "", 1)
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if len(identities) != 1 {
		t.Fatalf("expected 1 identity, got %d", len(identities))
	}
	if identities[0].FirstName != "Yeti" {
		t.Errorf("unexpected firstName: %q", identities[0].FirstName)
	}
}

func TestGenerator_ProviderError(t *testing.T) {
	stub := &stubProvider{err: fmt.Errorf("the AI is napping")}

	gen := NewGenerator(stub)
	_, err := gen.Generate(context.Background(), "Anything", "", 5)
	if err == nil {
		t.Fatal("expected error from provider failure")
	}
	if !strings.Contains(err.Error(), "napping") {
		t.Errorf("expected provider error in message, got: %v", err)
	}
}

func TestGenerator_NoJSONInResponse(t *testing.T) {
	stub := &stubProvider{response: "I refuse to play this silly game."}

	gen := NewGenerator(stub)
	_, err := gen.Generate(context.Background(), "Anything", "", 5)
	if err == nil {
		t.Fatal("expected error when no JSON in response")
	}
	if !strings.Contains(err.Error(), "no JSON object") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGenerator_InvalidBehavior(t *testing.T) {
	stub := &stubProvider{response: `{"personas": [
		{
			"firstName": "Doug",
			"lastName": "Confused",
			"age": 35,
			"state": "OR",
			"occupation": "Confused person",
			"email": "doug@gollm-test.example",
			"username": "doug",
			"password": "WhyAm!Here99",
			"description": "Doug is unsure what's happening.",
			"behavior": "extremely-online",
			"interests": ["confusion"],
			"goals": ["figure out what's going on"]
		}
	]}`}

	gen := NewGenerator(stub)
	_, err := gen.Generate(context.Background(), "Confused users", "", 1)
	if err == nil {
		t.Fatal("expected validation error for invalid behavior")
	}
	if !strings.Contains(err.Error(), "invalid behavior") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGenerator_MissingRequiredField(t *testing.T) {
	stub := &stubProvider{response: `{"personas": [
		{
			"firstName": "",
			"lastName": "Anonymous",
			"age": 40,
			"state": "TX",
			"occupation": "Mystery",
			"email": "anon@gollm-test.example",
			"username": "anon",
			"password": "Hidden!1234",
			"description": "Refuses to give a first name.",
			"behavior": "lurker",
			"interests": ["privacy"],
			"goals": ["stay anonymous"]
		}
	]}`}

	gen := NewGenerator(stub)
	_, err := gen.Generate(context.Background(), "Anonymous types", "", 1)
	if err == nil {
		t.Fatal("expected validation error for missing firstName")
	}
	if !strings.Contains(err.Error(), "firstName") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGenerator_RejectsZeroCount(t *testing.T) {
	gen := NewGenerator(&stubProvider{})
	_, err := gen.Generate(context.Background(), "Anything", "", 0)
	if err == nil {
		t.Fatal("expected error for count=0")
	}
}

func TestGenerator_RejectsEmptyBrief(t *testing.T) {
	gen := NewGenerator(&stubProvider{})
	_, err := gen.Generate(context.Background(), "   ", "", 5)
	if err == nil {
		t.Fatal("expected error for empty brief")
	}
}

func TestExtractJSONObject_NestedBraces(t *testing.T) {
	// Object with nested objects and strings containing braces.
	in := `prose before {"a": {"b": "has } in string"}, "c": [{"d": 1}]} prose after`
	got := extractJSONObject(in)
	want := `{"a": {"b": "has } in string"}, "c": [{"d": 1}]}`
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}
