package provider

// UnregisterForTest removes a registry entry. Lives in a _test.go file
// so it never ships in production binaries; exported so the external
// `package provider_test` registry tests can call it from t.Cleanup,
// keeping the global registry stable across `go test -count=N`.
func UnregisterForTest(prefix string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, prefix)
}
