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

# Bake in the user-content directories at known paths. `gollm serve`
# defaults --apps / --campaigns / --personas to these relative dirs
# from WORKDIR, so no flags are needed in CMD. Compose users can
# bind-mount over them for hot-edit during development.
COPY apps /app/apps
COPY campaigns /app/campaigns
COPY personas /app/personas

EXPOSE 8080

# nonroot UID 65532. Clerk JWT validation comes from the
# COHORT_CLERK_ISSUER env var (empty = dev mode, no auth).
USER nonroot

ENTRYPOINT ["/usr/local/bin/gollm"]
CMD ["serve", "--addr", ":8080"]
