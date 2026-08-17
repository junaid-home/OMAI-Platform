package main

import "testing"

func TestParseOriginsProducesExactOriginsAndHostPatterns(t *testing.T) {
	t.Parallel()

	origins, patterns, err := parseOrigins("https://app.example.test, http://localhost:4444")
	if err != nil {
		t.Fatalf("parseOrigins() error = %v", err)
	}
	if _, ok := origins["https://app.example.test"]; !ok {
		t.Fatalf("origins = %#v", origins)
	}
	if len(patterns) != 2 || patterns[0] != "app.example.test" || patterns[1] != "localhost:4444" {
		t.Fatalf("patterns = %#v", patterns)
	}
}

func TestParseOriginsRejectsPathsCredentialsAndNonHTTP(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"file:///tmp/socket", "https://user@example.test", "https://example.test/path", "https://example.test?query=true"} {
		if _, _, err := parseOrigins(value); err == nil {
			t.Fatalf("parseOrigins(%q) accepted an unsafe origin", value)
		}
	}
}
