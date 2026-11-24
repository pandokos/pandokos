package repo

import (
	"os/exec"
	"path"
	"regexp"
	"strings"
	"testing"
)

var composeImageRE = regexp.MustCompile(`(?m)^\s*image:\s*(\S.*?)\s*$`)

// composeImages returns, per Compose file tracked by git, its image: values.
func composeImages(t *testing.T) map[string][]string {
	t.Helper()
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = root(t)
	listed, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	out := map[string][]string{}
	for _, f := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		base := path.Base(f)
		if !strings.HasSuffix(base, ".yaml") && !strings.HasSuffix(base, ".yml") {
			continue
		}
		if !strings.HasPrefix(base, "compose") && !strings.HasPrefix(base, "docker-compose") && base != "override.yaml" {
			continue
		}
		var images []string
		for _, m := range composeImageRE.FindAllStringSubmatch(read(t, f), -1) {
			v := m[1]
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			images = append(images, strings.Trim(v, `"'`))
		}
		out[f] = images
	}
	return out
}
