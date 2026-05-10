# syntax=docker/dockerfile:1.7

# --- build ---
# Pinned to the Go minor version go.mod declares so the Dockerfile
# stays in lockstep with the toolchain the source actually compiles
# against. Bump alongside the `go` directive in go.mod.
FROM golang:1.26-alpine AS build
WORKDIR /src

# Module cache hits even when source changes — go.mod / go.sum land
# first so `go mod download` only re-runs when deps move.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a fully static binary, which is what
# distroless/static expects. -trimpath strips local filesystem paths
# from the binary so different machines produce identical artifacts.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gollm ./cmd/gollm

# --- run ---
# distroless/static:nonroot — no shell, no package manager, no /etc/passwd
# editor's cat photos. Smallest plausible runtime; smallest plausible CVE
# surface. The binary doesn't need libc since it's CGO-free.
FROM gcr.io/distroless/static:nonroot

WORKDIR /app
COPY --from=build /out/gollm /usr/local/bin/gollm

# The image ships data-free: gollm-qa is app-agnostic per CLAUDE.md,
# so no per-target YAML is baked in. `gollm serve` defaults
# --apps / --campaigns / --personas to those relative dirs (resolved
# from WORKDIR /app), so consumers must mount their content there:
#
#   docker run -v ./apps:/app/apps -v ./personas:/app/personas \
#     -v ./campaigns:/app/campaigns -p 8080:8080 gollm-qa
#
# Or pass --apps / --personas / --campaigns flags pointing elsewhere.
# Without mounts the server still boots and /health responds; the
# read-only listing endpoints just return empty until you mount data
# or POST runs with inline config/personas.

EXPOSE 8080

# distroless has no shell, no curl, no wget — only /usr/local/bin/gollm.
# Probing /health through the binary itself sidesteps that constraint
# and keeps the runtime image minimal. Cohort's compose uses this for
# `depends_on: engine: condition: service_healthy`.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/gollm", "healthcheck"]

# nonroot UID 65532. Clerk JWT validation comes from the
# COHORT_CLERK_ISSUER env var (empty = dev mode, no auth).
USER nonroot

ENTRYPOINT ["/usr/local/bin/gollm"]
CMD ["serve", "--addr", ":8080"]
