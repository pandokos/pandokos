package repo

import (
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestIDPImagesPinned is T-R38: every image of test/idp/compose.yaml is pinned by digest, the file
// has the images of the three providers (and Authentik's database), the tests of the providers fail
// rather than skip, and Dependabot watches
// the directory.
func TestIDPImagesPinned(t *testing.T) {
	var doc composeDoc
	if err := yaml.Unmarshal([]byte(read(t, "test/idp/compose.yaml")), &doc); err != nil {
		t.Fatal(err)
	}
	pinned := regexp.MustCompile(`^[a-z0-9./_-]+:[\w.-]+@sha256:[0-9a-f]{64}$`)
	if len(doc.Services) < 5 {
		t.Errorf("test/idp/compose.yaml has %d services, want Keycloak, Hydra and Authentik (server, worker, database)", len(doc.Services))
	}
	for name, s := range doc.Services {
		if !pinned.MatchString(s.Image) {
			t.Errorf("service %s: image %q is not name:tag@sha256:digest", name, s.Image)
		}
		for _, p := range s.Ports {
			if !strings.HasPrefix(p, "127.0.0.1:") {
				t.Errorf("service %s publishes %q", name, p)
			}
		}
	}
	for _, want := range []string{"quay.io/keycloak/keycloak:", "oryd/hydra:", "ghcr.io/goauthentik/server:", "postgres:"} {
		found := false
		for _, s := range doc.Services {
			found = found || strings.HasPrefix(s.Image, want)
		}
		if !found {
			t.Errorf("test/idp/compose.yaml has no image %s…", want)
		}
	}

	t.Run("Dependabot covers the directory", func(t *testing.T) {
		if dependabotEntries(t)[[2]string{"docker-compose", "/test/idp"}] {
			return
		}
		t.Error(`dependabot.yml has no entry "docker-compose /test/idp"`)
	})
	t.Run("the tests carry the tag and never skip", func(t *testing.T) {
		src := read(t, "test/idp/idp_test.go")
		if !strings.Contains(src, "//go:build idp") || strings.Contains(src, "t.Skip") || strings.Contains(src, "tb.Skip") {
			t.Error("test/idp/idp_test.go must carry the idp tag and never skip")
		}
	})
}
