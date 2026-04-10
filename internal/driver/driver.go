// Package driver defines the interface for interacting with a target application.
// Implementations include API-level (GraphQL/REST) and browser-level (Playwright).
package driver

import (
	"context"

	"github.com/VoterBloc/gollm-qa/internal/provider"
)

// Driver executes tool calls against a target application and returns results.
type Driver interface {
	// Tools returns the set of tools this driver makes available to agents.
	Tools() []provider.Tool

	// Execute runs a tool call and returns the result.
	Execute(ctx context.Context, call provider.ToolCall) (*provider.ToolResult, error)

	// Close cleans up any resources (browser sessions, connections, etc.).
	Close() error
}