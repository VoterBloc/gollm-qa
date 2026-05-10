package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// healthcheckCmd probes a running gollm server's /health endpoint and
// exits 0 (success) or 1 (failure). Designed for the distroless
// HEALTHCHECK case: the runtime image has no shell, no curl, no wget —
// only /usr/local/bin/gollm. Probing through the binary itself sidesteps
// that constraint without bloating the image.
//
// Resolution order for the target URL: --url wins outright; otherwise
// --addr's port is folded into http://localhost:<port>/health; otherwise
// http://localhost:8080/health. The localhost host is intentional even
// when --addr is "0.0.0.0:8080" — this command runs inside the same
// container as the server, so the bind address doesn't tell us where
// to probe.
func healthcheckCmd(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	var (
		addr    string
		url     string
		timeout time.Duration
	)
	fs.StringVar(&addr, "addr", "", "server bind address (e.g. :8080); used to build http://localhost<addr>/health when --url is empty")
	fs.StringVar(&url, "url", "", "full URL to probe (overrides --addr)")
	fs.DurationVar(&timeout, "timeout", 2*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	target := resolveHealthcheckURL(url, addr)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("building request for %s: %w", target, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s failed: %w", target, err)
	}
	defer resp.Body.Close()

	// /health uses status code as the source of truth — body content
	// is informational only. The server returns non-2xx when unhealthy,
	// so a 2xx here is a real "ready" signal regardless of body shape.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("probe %s returned status %d", target, resp.StatusCode)
	}
	return nil
}

// resolveHealthcheckURL applies the --url > --addr > default cascade.
// addr can arrive as ":8080", "0.0.0.0:8080", "[::]:8080", or just
// "8080" — the host (if any) is dropped because we probe localhost
// regardless of the bind, and only the port matters.
func resolveHealthcheckURL(urlFlag, addr string) string {
	if urlFlag != "" {
		return urlFlag
	}
	port := portFromAddr(addr)
	if port == "" {
		port = "8080"
	}
	return "http://localhost:" + port + "/health"
}

// portFromAddr returns the port from a Go-style listen address.
// Accepts the shapes net.Listen accepts (":8080", "host:8080",
// "0.0.0.0:8080", "[::]:8080") via net.SplitHostPort. Anything that
// doesn't parse cleanly returns "" so the caller falls back to the
// default port — better than threading malformed input into the URL
// only to surface as an opaque request-build error later.
func portFromAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}
