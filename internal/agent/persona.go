package agent

import (
	"fmt"
	"strings"
)

// Persona defines a synthetic user's identity and behavior.
// App-specific details live in the freeform Description field —
// Gollm itself enforces no schema beyond the basics.
type Persona struct {
	// Name identifies the agent in logs and reports.
	Name string `yaml:"name" json:"name"`

	// Description is freeform text that becomes the persona section of the
	// system prompt. Write it however makes sense for your target app.
	Description string `yaml:"description" json:"description"`

	// Goals are what the agent tries to accomplish. The agent loop uses
	// these to decide when the session is complete.
	Goals []string `yaml:"goals" json:"goals"`

	// Behavior controls pacing: how active and engaged the agent is.
	Behavior Behavior `yaml:"behavior" json:"behavior"`

	// Tags are arbitrary key-value pairs for filtering and reporting.
	// No schema enforced — use whatever makes sense for your app.
	Tags map[string]string `yaml:"tags,omitempty" json:"tags,omitempty"`

	// Credentials are used to authenticate the agent with the target app.
	// Never serialized to JSON (reports, session logs, etc.).
	Credentials Credentials `yaml:"credentials" json:"-"`
}

// Credentials holds login details for a synthetic user.
type Credentials struct {
	Identifier string `yaml:"identifier" json:"-"`
	Password   string `yaml:"password" json:"-"`
}

// Behavior controls how active and engaged a synthetic user is.
type Behavior string

const (
	BehaviorEngaged  Behavior = "engaged"
	BehaviorModerate Behavior = "moderate"
	BehaviorLurker   Behavior = "lurker"
)

// SystemPrompt builds the LLM system prompt for this persona.
func (p *Persona) SystemPrompt() string {
	var b strings.Builder

	b.WriteString("You are a synthetic user interacting with a web application. Stay in character at all times.\n\n")

	b.WriteString("## Who You Are\n")
	b.WriteString(fmt.Sprintf("Name: %s\n", p.Name))
	b.WriteString(p.Description)
	b.WriteString("\n")

	if len(p.Goals) > 0 {
		b.WriteString("\n## Your Goals\n")
		b.WriteString("Complete these goals by interacting with the application:\n")
		for _, goal := range p.Goals {
			b.WriteString("- " + goal + "\n")
		}
	}

	b.WriteString("\n## Behavior Style\n")
	switch p.Behavior {
	case BehaviorEngaged:
		b.WriteString("You are an active, engaged user. You browse extensively, post frequently, comment on others' content, and participate in discussions. You're enthusiastic and opinionated.\n")
	case BehaviorLurker:
		b.WriteString("You mostly observe. You browse and read but rarely post or comment. When you do interact, it's brief. You might join groups but don't actively participate.\n")
	case BehaviorModerate:
		b.WriteString("You're a regular but not obsessive user. You browse, occasionally post or comment when something catches your eye, and join groups that match your interests.\n")
	default:
		b.WriteString("You interact naturally with the application at your own pace.\n")
	}

	b.WriteString("\n## Instructions\n")
	b.WriteString("- Use the available tools to interact with the application\n")
	b.WriteString("- Write posts and comments in your own voice based on your persona\n")
	b.WriteString("- If you encounter something confusing or broken, note it as a UX observation\n")
	b.WriteString("- Work toward your goals but behave naturally — don't rush through them mechanically\n")
	b.WriteString("- When you've completed all your goals or have nothing left to do, say so\n")

	return b.String()
}