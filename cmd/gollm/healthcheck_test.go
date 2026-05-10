package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestResolveHealthcheckURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		addr string
		want string
	}{
		{"default everything", "", "", "http://localhost:8080/health"},
		{"colon-prefixed addr", "", ":8080", "http://localhost:8080/health"},
		{"host-form addr", "", "0.0.0.0:9090", "http://localhost:9090/health"},
		{"ipv6 listen addr", "", "[::]:7000", "http://localhost:7000/health"},
		{"malformed addr falls back to default", "", "abc", "http://localhost:8080/health"},
		{"bare port falls back (not a valid Go listen addr)", "", "9001", "http://localhost:8080/health"},
		{"explicit url wins over addr", "https://yeti.example/health", ":9000", "https://yeti.example/health"},
		{"explicit url alone", "https://bigfoot.test/probe", "", "https://bigfoot.test/probe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveHealthcheckURL(tc.url, tc.addr); got != tc.want {
				t.Errorf("resolveHealthcheckURL(%q, %q) = %q, want %q", tc.url, tc.addr, got, tc.want)
			}
		})
	}
}

func TestHealthcheckCmd_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	if err := healthcheckCmd([]string{"--url", server.URL}); err != nil {
		t.Errorf("healthcheckCmd against healthy server returned error: %v", err)
	}
}

func TestHealthcheckCmd_AddrFlagEndToEnd(t *testing.T) {
	// httptest binds 127.0.0.1:<random>, which lines up with the
	// localhost-always probe assumption. Capture the port and feed it
	// in via --addr so the resolution cascade is exercised through
	// the same code path the Dockerfile HEALTHCHECK takes — only the
	// port flows from the flag.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parsing test server URL: %v", err)
	}

	if err := healthcheckCmd([]string{"--addr", ":" + u.Port()}); err != nil {
		t.Errorf("healthcheckCmd --addr=:%s returned error: %v", u.Port(), err)
	}
}

func TestHealthcheckCmd_Non2xxReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "abominable", http.StatusInternalServerError)
	}))
	defer server.Close()

	err := healthcheckCmd([]string{"--url", server.URL})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code, got %v", err)
	}
}

func TestHealthcheckCmd_NetworkErrorReturnsError(t *testing.T) {
	// No listener — the request should fail at connect.
	err := healthcheckCmd([]string{
		"--url", "http://127.0.0.1:1/health", // RFC 1700 reserved port; nothing listens here
		"--timeout", "300ms",
	})
	if err == nil {
		t.Fatal("expected error for unreachable URL")
	}
	if !strings.Contains(err.Error(), "probe") {
		t.Errorf("error should describe the probe, got %v", err)
	}
}

func TestHealthcheckCmd_TimeoutFiresWithinDeadline(t *testing.T) {
	// Server that hangs forever — confirms the --timeout flag wires
	// through to the request context.
	hang := make(chan struct{})
	defer close(hang)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	err := healthcheckCmd([]string{
		"--url", server.URL,
		"--timeout", "150ms",
	})
	if err == nil {
		t.Fatal("expected timeout error against a hanging server")
	}
}
