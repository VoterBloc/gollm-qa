# Gollm

AI-driven synthetic users for QA, data seeding, and UX evaluation.

Point Gollm at any web app, define personas, and get realistic usage data, E2E test coverage, and UX evaluation reports — all at once.

## What it does

Gollm creates AI-powered synthetic users that interact with your application as real people would. Each agent gets a persona (demographics, interests, goals, behavior style) and autonomously navigates your app — browsing, posting, commenting, completing flows — while logging everything it does and observes.

Three modes of value from a single run:

1. **Data seeding** — generate organic, realistic activity across dozens of users simultaneously
2. **E2E testing** — if an agent can't complete a flow, that's a real bug
3. **UX evaluation** — agents log observations as they navigate: confusing UI, missing affordances, unexpected flows, error states (with screenshots)

## Architecture

```
gollm/
├── cmd/gollm/                # CLI entrypoint
├── internal/
│   ├── agent/                # Agent loop, decision engine, persona state
│   ├── provider/             # LLM abstraction (Claude, GPT, etc.)
│   ├── driver/
│   │   └── api/              # API-level driver (GraphQL, REST) — pure Go
│   ├── reporter/             # Session reports, UX observations
│   └── config/               # App config, persona loading
├── browser/                  # TypeScript — thin Playwright bridge (sidecar)
├── personas/                 # YAML persona definitions
└── reports/                  # Output: session logs, screenshots, UX reports
```

**Go core** handles agent orchestration, concurrency (goroutines for parallel agents), LLM API calls, persona management, API-level drivers, reporting, and the CLI.

**TypeScript browser bridge** is a small sidecar process that wraps Playwright and exposes browser actions over local RPC (gRPC or HTTP). The Go agent sends commands like `navigate("/blocs")`, `click("Join Bloc")`, `screenshot()` and gets results back. Zero decision-making logic lives in the TS layer. When running API-only mode, the sidecar isn't started at all.

## Drivers

Agents interact with target apps through two driver types that implement the same interface:

- **API driver** — sends requests directly to GraphQL or REST endpoints. Fast, lightweight, good for bulk data generation.
- **Browser driver** — controls a real browser via the Playwright sidecar. Slower but gives you E2E coverage and UX observations.

Agents can use either driver (or both) depending on the run configuration.

## LLM Providers

Gollm supports multiple LLM backends through a provider abstraction:

- **Claude** (primary) — strong at nuanced UX observations and natural language generation
- **GPT-4o** — alternative backend, different "personality" for variety in synthetic users
- Additional providers (Gemini, Llama, etc.) can be added via the same interface

## Personas

Personas are YAML files that define a synthetic user's identity and behavior:

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

## Reports

Each agent session produces a structured JSON report:

- Actions taken (with timestamps)
- Goals achieved vs. failed
- Errors encountered (with screenshots in browser mode)
- UX observations: confusing navigation, missing affordances, unexpected step counts, hard-to-find elements

## Usage

```bash
# Run 10 agents against a target app via API
gollm run --agents 10 --driver api --target https://app.example.com

# Run agents in browser mode (starts Playwright sidecar)
gollm run --agents 5 --driver browser --target https://app.example.com

# Use a specific persona directory
gollm run --agents 10 --personas ./my-personas --target https://app.example.com
```

## Development

### Prerequisites

- Go 1.26+
- Node.js 20+ (only for browser mode)

### Build

```bash
go build -o gollm ./cmd/gollm
```

### Browser sidecar (only needed for browser mode)

```bash
cd browser && npm install
```

## Status

Early development. See [VoterBloc/voterbloc#741](https://github.com/VoterBloc/voterbloc/issues/741) for the original design discussion.

## License

TBD