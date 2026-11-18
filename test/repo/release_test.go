package repo

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestReleaseR1 is T-R39: docs/releases/v0.1.0.md names each of the eleven R1 criteria of
// requirements section 7 with a test ID that exists in the repository (or a document that does),
// and the roadmap and the README agree with it on the status. It is the acceptance of R1 and stays
// on that page whatever the newest release is; TestLatestReleaseIsPinned follows the newest one.
func TestReleaseR1(t *testing.T) {
	page := read(t, "docs/releases/v0.1.0.md")

	// The test IDs of the repository: every "T-123" and "T-R12" in a Go test file.
	idRE := regexp.MustCompile(`\bT-(?:R)?\d+\b`)
	known := map[string]bool{}
	err := filepath.Walk(root(t), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "local") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(path, "_test.go") {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, id := range idRE.FindAllString(string(b), -1) {
				known[id] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("one row per criterion, each with an existing test ID", func(t *testing.T) {
		rowRE := regexp.MustCompile(`^\| (\d+) \|`)
		seen := map[string]bool{}
		for _, line := range strings.Split(page, "\n") {
			m := rowRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			seen[m[1]] = true
			ids := idRE.FindAllString(line, -1)
			ok := false
			for _, id := range ids {
				if known[id] && id != "T-R39" {
					ok = true
				}
			}
			if m[1] == "11" && strings.Contains(line, "T-R39") && strings.Contains(line, "security-model.md") {
				ok = true // a document criterion: T-R39 below checks the section
			}
			if !ok {
				t.Errorf("criterion %s names no test ID that exists in the repository: %s", m[1], line)
			}
			for _, id := range ids {
				if id != "T-R39" && !known[id] {
					t.Errorf("criterion %s names %s, which no test file mentions", m[1], id)
				}
			}
			if strings.Count(line, "|") != 7 {
				t.Errorf("criterion %s has no criterion, evidence, command, result and place columns", m[1])
			}
		}
		for i := 1; i <= 11; i++ {
			if !seen[strconv.Itoa(i)] {
				t.Errorf("the acceptance table has no row %d", i)
			}
		}
	})

	t.Run("the criteria are quoted from the requirements", func(t *testing.T) {
		req := read(t, "docs/requirements/README.md")
		i := strings.Index(req, "**R1 — v0.1.**")
		if i < 0 {
			t.Fatal("requirements section 7 has no R1 paragraph")
		}
		j := strings.Index(req[i:], "**R2 — v0.2.**")
		r1 := strings.Join(strings.Fields(req[i:i+j]), " ")
		for n := 1; n <= 11; n++ {
			if !strings.Contains(r1, "("+strconv.Itoa(n)+")") {
				t.Errorf("requirements section 7 R1 has no criterion (%d)", n)
			}
		}
		for _, want := range []string{
			"`docker compose up` gives a working installation with a persistent database",
			"A BYOK credential is stored encrypted and never returned",
			"The image is minimal, scanned, signed and ships an SBOM",
		} {
			if !strings.Contains(page, want) {
				t.Errorf("the release page does not quote %q", want)
			}
		}
	})

	t.Run("the limits of protection against host, database and keys are in the security model", func(t *testing.T) {
		if !strings.Contains(read(t, "docs/architecture/security-model.md"), "## What Pandokos does not protect against") {
			t.Error("security-model.md has no section 'What Pandokos does not protect against'")
		}
	})

	t.Run("the page carries its sections", func(t *testing.T) {
		for _, want := range []string{
			"not for production", "## What is in", "## Known limits", "## Upgrade notes",
			"## How to verify the image", "## Maintainer actions outside the repository",
			"What was verified where", "0001 to 0008",
		} {
			if !strings.Contains(page, want) {
				t.Errorf("the release page lacks %q", want)
			}
		}
		for _, link := range regexp.MustCompile(`\]\((\.\./[^)#]+)`).FindAllStringSubmatch(page, -1) {
			if _, err := os.Stat(filepath.Join(root(t), "docs", "releases", link[1])); err != nil {
				t.Errorf("the release page links %s, which does not exist", link[1])
			}
		}
	})

	t.Run("roadmap and README agree", func(t *testing.T) {
		road := read(t, "planning/roadmap.md")
		if !regexp.MustCompile(`\| \*\*v0\.1\.0\*\* \|[^\n]*\| 9 \| 9 done \|`).MatchString(road) {
			t.Error(`the roadmap row of v0.1.0 does not read "9 done"`)
		}
		readme := read(t, "README.md")
		for _, want := range []string{"v0.1.0", "docs/releases/v0.1.0.md", "docs/operations/tokens.md", "not for production"} {
			if !strings.Contains(readme, want) {
				t.Errorf("the README status lacks %q", want)
			}
		}
		if strings.Contains(readme, "not yet released") {
			t.Error("the README still says that v0.1 is not yet released")
		}
	})

	t.Run("the sprint is closed", func(t *testing.T) {
		sprint := read(t, "planning/epics/04-identity-and-rbac/4.1-trusted-token-issuers/sprint.md")
		if !regexp.MustCompile(`(?m)^status: done`).MatchString(sprint) {
			t.Error("sprint 4.1 is not done")
		}
	})
}

// latestRelease is the highest version, without a pre-release suffix, that has a page in
// docs/releases (v0.1.1.md gives "0.1.1").
func latestRelease(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root(t), "docs", "releases"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)\.md$`)
	best, bestN := "", [3]int{-1, -1, -1}
	for _, e := range entries {
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		var n [3]int
		for i := range n {
			n[i], _ = strconv.Atoi(m[i+1])
		}
		if n[0] > bestN[0] || n[0] == bestN[0] && (n[1] > bestN[1] || n[1] == bestN[1] && n[2] > bestN[2]) {
			best, bestN = m[1]+"."+m[2]+"."+m[3], n
		}
	}
	if best == "" {
		t.Fatal("docs/releases has no page of a release")
	}
	return best
}

// TestLatestReleaseIsPinned checks that what a reader runs and reads names the newest release, the
// one whose page is in docs/releases: the quick start clones its tag, the Compose file runs the
// moving tag of its minor version, and the README, the Compose guide and the security policy name
// it. A release page added
// without these updated fails here, so the documents are aligned before the tag is pushed.
func TestLatestReleaseIsPinned(t *testing.T) {
	v := latestRelease(t)
	tag := "v" + v
	minor := v[:strings.LastIndex(v, ".")] // 0.1.1 gives 0.1, the tag that the release workflow moves
	has := func(t *testing.T, file, want string) {
		t.Helper()
		if !strings.Contains(read(t, file), want) {
			t.Errorf("%s does not contain %q (newest release page: %s)", file, want, tag)
		}
	}

	t.Run("README", func(t *testing.T) {
		has(t, "README.md", "docs/releases/"+tag+".md")
		has(t, "README.md", "git clone --depth 1 --branch "+tag+" https://github.com/pandokos/pandokos.git")
		has(t, "README.md", "`ghcr.io/pandokos/pandokos:"+minor+"`")
		if strings.Contains(read(t, "README.md"), "git clone --depth 1 https://github.com/pandokos/pandokos.git") {
			t.Error("the quick start of the README clones the default branch, not the tag of the release")
		}
	})

	t.Run("Compose file and guide", func(t *testing.T) {
		compose := read(t, "deploy/compose/compose.yaml")
		if n := strings.Count(compose, "${PANDOKOS_IMAGE:-ghcr.io/pandokos/pandokos:"+minor+"}"); n != 2 {
			t.Errorf("deploy/compose/compose.yaml names the image %s %d times, want 2 (migrate and pandokos)", minor, n)
		}
		has(t, "docs/operations/compose.md", "The default `ghcr.io/pandokos/pandokos:"+minor+"` follows the latest patch release")
		has(t, "docs/operations/compose.md", "(today `"+tag+"`)")
		if strings.Contains(read(t, "docs/operations/compose.md"), "does not exist yet") {
			t.Error("the Compose guide says the image does not exist yet")
		}
		// The release workflow pushes the version, and for a release that is not a pre-release
		// also the minor version and latest.
		has(t, ".github/workflows/release.yml", "${IMAGE}:${VERSION%.*}")
	})

	t.Run("security policy and release page", func(t *testing.T) {
		has(t, "SECURITY.md", "`"+tag+"`")
		has(t, "docs/releases/"+tag+".md", "Push the signed tag `"+tag+"`")
		has(t, "docs/releases/"+tag+".md", "ghcr.io/pandokos/pandokos:"+v)
		has(t, "CONTRIBUTING.md", "### Releasing")
	})
}
