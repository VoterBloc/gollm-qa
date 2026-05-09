package agent

import "time"

// EventKind identifies the shape of an Event's Payload.
type EventKind string

const (
	// EventSessionStart fires once at the top of Agent.Run, before any
	// authentication or LLM calls. Payload is nil.
	EventSessionStart EventKind = "session_start"

	// EventStep fires after each completed step's tool calls. Payload is
	// []Action — the actions taken during this step (not the full session
	// history).
	EventStep EventKind = "step"

	// EventObservation fires when the agent invokes the
	// report_ux_observation builtin. Payload is UXNote.
	EventObservation EventKind = "observation"

	// EventError fires when the agent's loop hits a fatal error
	// (registration, login, provider call). Payload is AgentError.
	EventError EventKind = "error"

	// EventSessionEnd fires once at the end of Agent.Run, just before
	// returning. Payload is *Session — the full session report.
	EventSessionEnd EventKind = "session_end"
)

// Event is emitted by the agent during Run when a Config.OnEvent callback
// is registered. Subscribers type-assert Payload based on Kind.
type Event struct {
	Kind    EventKind `json:"kind"`
	Step    int       `json:"step,omitempty"`
	At      time.Time `json:"at"`
	Payload any       `json:"payload,omitempty"`
}

// EventCallback is invoked synchronously from inside Agent.Run as notable
// things happen. Callbacks must not block — the agent loop blocks while
// the callback runs. If the callback needs to do I/O (e.g. write to an
// SSE stream), buffer through a channel.
type EventCallback func(Event)
