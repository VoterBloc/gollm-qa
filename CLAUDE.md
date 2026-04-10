# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Gollm is an AI-driven synthetic user platform. Point it at any web app, define personas, and get realistic usage data, E2E test coverage, and UX evaluation reports. It creates AI-powered agents that interact with a target application as real users would — browsing, posting, commenting, completing flows — while logging everything they do and observe.

Gollm is app-agnostic. VoterBloc is the first target app, but the tool itself has no coupling to VoterBloc. All target app details (URL, auth flow, API schema, page structure) live in configuration files.

The name is a portmanteau of Go + LLM (and a golem — a constructed entity brought to life to perform tasks).

**Origin issue:** [VoterBloc/voterbloc#741](https://github.com/VoterBloc/voterbloc/issues/741)

## Build Commands

```bash
# Build the CLI binary
go build -o gollm ./cmd/gollm

# Run tests
go test ./...

# Run a specific package's tests
go test ./internal/agent/...

# Vet
go vet ./...
```

### Browser sidecar (only needed for browser mode)

```bash
cd browser && npm install
cd browser && npm run build
```

## Architecture

```
gollm-qa/
├── cmd/gollm/                # CLI entrypoint
├── internal/
│   ├── agent/                # Agent loop, decision engine, persona state
│   ├── provider/             # LLM abstraction (Claude, GPT, etc.)
│   ├── driver/
│   │   └── api/              # API-level driver (GraphQL, REST) — pure Go
│   ├── reporter/             # Session reports, UX observations
│   └── config/               # App config, persona loading
├── browser/                  # TypeScript — thin Playwright bridge (sidecar)
│   └── src/
│       ├── server.ts         # RPC server exposing Playwright actions
│       └── actions.ts        # Navigate, click, fill, screenshot, read page
├── personas/                 # YAML persona definitions
└── reports/                  # Output: session logs, screenshots, UX reports
```

### Go core

The Go code handles everything: agent orchestration, concurrency (goroutines for parallel agents), LLM API calls, persona management, API-level drivers, reporting, and the CLI.

### TypeScript browser bridge

A small sidecar process that wraps Playwright and exposes browser actions over local RPC (gRPC or HTTP). The Go agent sends commands like `navigate("/blocs")`, `click("Join Bloc")`, `screenshot()` and gets results back. Zero decision-making logic lives in the TS layer — it's a remote control for the browser. When running API-only mode, the sidecar isn't started at all.

### Design Principles

- **App-agnostic configuration.** Gollm knows nothing about any specific app. Target app details (URL, auth flow, API schema, page structure) live in config files that users provide.
- **Driver abstraction.** The Playwright driver and API driver implement the same interface, so agents can operate at either level (or both).
- **Persona-as-data.** Personas are YAML files with demographics, interests, goals, and behavior tendencies. Easy to create, share, or generate.
- **Structured output.** Session reports are machine-readable JSON with human-readable summaries. Could feed into dashboards, CI pipelines, or issue trackers.
- **LLM provider abstraction.** Thin adapter per provider (Claude, GPT, etc.) behind a common interface. Swapping or adding models should be trivial.

## Drivers

Agents interact with target apps through two driver types that implement the same interface:

- **API driver** (`internal/driver/api/`) — sends requests directly to GraphQL or REST endpoints. Fast, lightweight, good for bulk data generation. Pure Go.
- **Browser driver** — controls a real browser via the Playwright sidecar. Slower but gives E2E coverage and UX observations.

## LLM Providers

Multiple backends supported through the provider abstraction in `internal/provider/`:

- **Claude** (primary) — strong at nuanced UX observations and natural language generation
- **GPT-4o** — alternative backend, different "personality" for variety in synthetic users
- Additional providers (Gemini, Llama, etc.) can be added via the same interface

## Personas

Personas are YAML files in `personas/` that define a synthetic user's identity and behavior:

```yaml
name: Margaret Chen
age: 62
state: OH
occupation: Retired teacher
interests: [education funding, school board policy, local politics]
behavior: engaged        # engaged | lurker | moderate
verified_voter: true
goals:
  - Find and join an education-focused bloc
  - Post about school funding in her district
  - Comment on other members' posts
```

## Environment Variables

API keys and secrets for local development go in a `.env` file (not committed):

```
ANTHROPIC_API_KEY=...
OPENAI_API_KEY=...
```

## Code Quality

- **All files must end with a trailing newline.**
- **Tests required.** New functionality needs test coverage.
- **Test values should be amusing.** Don't use boring placeholder values like `"test"` or `"some text"`. Use ridiculous/funny values — `"Bigfoot Appreciation Society"`, `"fishsticks@example.com"`, `"Definitely Not A Real Senator"`, etc.
- **No god packages.** Keep packages focused on a single responsibility. If a package is doing too many things, split it up.
- **Interfaces over concrete types.** Define interfaces at the consumer, not the provider. Keep interfaces small.
- **Errors are values.** Use Go's error handling idioms — wrap errors with context via `fmt.Errorf("doing thing: %w", err)`, don't panic.
- **Concurrency via goroutines.** Agent orchestration should use goroutines and channels. Don't pull in heavy concurrency frameworks.
- **No magic strings.** Use constants and typed enums. No `"active"` scattered through code.