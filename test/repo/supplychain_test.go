package repo

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
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

// jobBlock returns the text of a job of a workflow: from its line at two spaces of indentation to
// the next job.
func jobBlock(t *testing.T, workflow, text, name string) string {
	t.Helper()
	i := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + `:\s*$`).FindStringIndex(text)
	if i == nil {
		t.Fatalf("%s has no job %s", workflow, name)
	}
	rest := text[i[1]:]
	if j := regexp.MustCompile(`(?m)^  [A-Za-z0-9_-]+:\s*$`).FindStringIndex(rest); j != nil {
		rest = rest[:j[0]]
	}
	return text[i[0]:i[1]] + rest
}

// hasJob reports whether the workflow has a job of that name.
func hasJob(text, name string) bool {
	return regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + `:\s*$`).MatchString(text)
}

var fuzzTimeRE = regexp.MustCompile(`make fuzz FUZZTIME=(\d+[smh])\b`)

// TestFuzzRunsInCI is T-R92 (sprint 12.2, section 5.10): ci.yml has a job fuzz that runs make fuzz
// with a FUZZTIME, without continue-on-error; scheduled.yml has a schedule trigger and a job that
// runs make fuzz with a longer one; no workflow names a fuzz target (the Makefile is the one list).
func TestFuzzRunsInCI(t *testing.T) {
	wf := workflows(t)
	ci, ok := wf["ci.yml"]
	if !ok {
		t.Fatal("no ci.yml")
	}
	job := jobBlock(t, "ci.yml", ci, "fuzz")
	m := fuzzTimeRE.FindStringSubmatch(job)
	if m == nil {
		t.Fatalf("the job fuzz of ci.yml does not run make fuzz FUZZTIME=<duration>:\n%s", job)
	}
	if strings.Contains(job, "continue-on-error") {
		t.Error("the job fuzz of ci.yml has continue-on-error: a crash must fail the job")
	}
	if !strings.Contains(job, "timeout-minutes:") {
		t.Error("the job fuzz of ci.yml has no timeout-minutes")
	}
	short, err := time.ParseDuration(m[1])
	if err != nil {
		t.Fatal(err)
	}

	sched, ok := wf["scheduled.yml"]
	if !ok {
		t.Fatal("no scheduled.yml")
	}
	if !regexp.MustCompile(`(?m)^  schedule:\s*$`).MatchString(sched) || !strings.Contains(sched, "cron:") {
		t.Error("scheduled.yml has no schedule trigger")
	}
	sm := fuzzTimeRE.FindStringSubmatch(jobBlock(t, "scheduled.yml", sched, "fuzz"))
	if sm == nil {
		t.Fatal("the job fuzz of scheduled.yml does not run make fuzz FUZZTIME=<duration>")
	}
	if long, err := time.ParseDuration(sm[1]); err != nil || long <= short {
		t.Errorf("the nightly FUZZTIME %s is not longer than the one of the pull request %s", sm[1], m[1])
	}
	if strings.Contains(jobBlock(t, "scheduled.yml", sched, "fuzz"), "continue-on-error") {
		t.Error("the job fuzz of scheduled.yml has continue-on-error")
	}
	for name, text := range wf {
		if strings.Contains(text, "-fuzz ") || strings.Contains(text, "go test -fuzz") {
			t.Errorf("%s names a fuzz target: the workflows call make fuzz", name)
		}
	}
}

var (
	pinnedImageRE = regexp.MustCompile(`^[^\s:@$]+(:[^\s@$]+)?@sha256:[0-9a-f]{64}$`)
	// The two variables of the Pandokos image that Compose files may hold (T-R93).
	imageVariableRE = regexp.MustCompile(`^\$\{PANDOKOS_IMAGE:-ghcr\.io/pandokos/pandokos:[0-9]+\.[0-9]+\}$|^\$\{PANDOKOS_TEST_IMAGE:\?\}$`)
	goRunRE         = regexp.MustCompile(`\bgo (run|install)\s+((?:-[^\s]+\s+)*)(\S+)`)
	exactVersionRE  = regexp.MustCompile(`@v\d+\.\d+\.\d+([-+][0-9A-Za-z.+-]+)?$`)
)

// TestEverythingPinned is T-R93: every image of every Compose file of the repository and every FROM
// of every Dockerfile is name:tag@sha256:<64 hex>, except the two variables of the Pandokos image;
// every go run and go install of a module in a workflow or the Makefile names an exact version; no
// workflow uses @latest, @main or @master anywhere.
func TestEverythingPinned(t *testing.T) {
	t.Run("Compose images", func(t *testing.T) {
		images := composeImages(t)
		n := 0
		for file, list := range images {
			for _, img := range list {
				n++
				if !pinnedImageRE.MatchString(img) && !imageVariableRE.MatchString(img) {
					t.Errorf("%s: the image %q is not name:tag@sha256:<digest>", file, img)
				}
			}
		}
		if n < 10 {
			t.Errorf("%d images in %d Compose files: the test would prove nothing", n, len(images))
		}
	})

	t.Run("Dockerfiles", func(t *testing.T) {
		cmd := exec.Command("git", "ls-files")
		cmd.Dir = root(t)
		listed, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		froms := 0
		for _, f := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
			base := path.Base(f)
			if base != "Dockerfile" && !strings.HasPrefix(base, "Dockerfile.") && !strings.HasSuffix(base, ".Dockerfile") {
				continue
			}
			stages := map[string]bool{"scratch": true}
			for _, line := range strings.Split(read(t, f), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
					continue
				}
				i := 1
				for strings.HasPrefix(fields[i], "--") {
					i++
				}
				img := fields[i]
				if len(fields) > i+2 && strings.EqualFold(fields[i+1], "AS") {
					defer func(name string) { stages[strings.ToLower(name)] = true }(fields[i+2])
				}
				if stages[strings.ToLower(img)] {
					continue
				}
				froms++
				if !pinnedImageRE.MatchString(img) {
					t.Errorf("%s: FROM %s is not name:tag@sha256:<digest>", f, img)
				}
			}
		}
		if froms == 0 {
			t.Error("no FROM of any Dockerfile: the test would prove nothing")
		}
	})

	t.Run("modules run by version", func(t *testing.T) {
		makefile := read(t, "Makefile")
		vars := map[string]string{}
		for _, m := range regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)\s*:?=\s*(\S+)\s*$`).FindAllStringSubmatch(makefile, -1) {
			vars[m[1]] = m[2]
		}
		sources := map[string]string{"Makefile": makefile}
		for name, text := range workflows(t) {
			sources[".github/workflows/"+name] = text
		}
		runs := 0
		for file, text := range sources {
			for _, m := range goRunRE.FindAllStringSubmatch(text, -1) {
				target := m[3]
				if v, ok := strings.CutPrefix(target, "$("); ok {
					target = vars[strings.TrimSuffix(v, ")")]
				}
				if target == "" || strings.HasPrefix(target, "./") || strings.HasPrefix(target, "-") || strings.HasPrefix(target, "$") {
					continue // a package of this repository, or a flag
				}
				runs++
				if !exactVersionRE.MatchString(target) {
					t.Errorf("%s: go %s %s does not name an exact version", file, m[1], target)
				}
			}
			for _, bad := range []string{"@latest", "@main", "@master"} {
				if strings.Contains(text, bad) {
					t.Errorf("%s uses %s", file, bad)
				}
			}
		}
		if runs < 3 {
			t.Errorf("%d modules run by version: the test would prove nothing", runs)
		}
	})
}

// dependabotEntries returns the (ecosystem, directory) pairs of dependabot.yml.
func dependabotEntries(t *testing.T) map[[2]string]bool {
	t.Helper()
	out := map[[2]string]bool{}
	var eco string
	for _, line := range strings.Split(read(t, ".github/dependabot.yml"), "\n") {
		l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if v, ok := strings.CutPrefix(l, "package-ecosystem:"); ok {
			eco = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(l, "directory:"); ok {
			out[[2]string{eco, strings.TrimSpace(v)}] = true
		}
	}
	return out
}

// TestDependabotCoverage is T-R94: every directory that holds a go.mod, a Compose file with a
// pinned image, a Dockerfile, a requirements.txt or a package.json, and .github/workflows, has an
// entry of the right ecosystem in dependabot.yml, and no entry names a directory that does not
// exist.
func TestDependabotCoverage(t *testing.T) {
	entries := dependabotEntries(t)
	if len(entries) < 5 {
		t.Fatalf("%d entries read from dependabot.yml", len(entries))
	}
	dir := func(f string) string {
		d := path.Dir(f)
		if d == "." {
			return "/"
		}
		return "/" + d
	}
	need := map[[2]string]string{{"github-actions", "/"}: ".github/workflows"}
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = root(t)
	listed, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		base := path.Base(f)
		switch {
		case base == "go.mod":
			need[[2]string{"gomod", dir(f)}] = f
		case base == "requirements.txt":
			need[[2]string{"pip", dir(f)}] = f
		case base == "package.json":
			need[[2]string{"npm", dir(f)}] = f
		case base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile."):
			need[[2]string{"docker", dir(f)}] = f
		}
	}
	for f, images := range composeImages(t) {
		for _, img := range images {
			if strings.Contains(img, "@sha256:") {
				need[[2]string{"docker-compose", dir(f)}] = f
			}
		}
	}
	var missing []string
	for key, why := range need {
		if !entries[key] {
			missing = append(missing, key[0]+" "+key[1]+" (for "+why+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("dependabot.yml has no entry for %s", m)
	}
	for key := range entries {
		if st, err := os.Stat(filepath.Join(root(t), filepath.FromSlash(strings.TrimPrefix(key[1], "/")))); err != nil || !st.IsDir() {
			t.Errorf("dependabot.yml names the directory %s (%s), which does not exist", key[1], key[0])
		}
	}
}

var verifyCommandRE = regexp.MustCompile(`(?m)\b(cosign (sign|verify|verify-attestation|attest)|gh attestation (verify|download))\b[^\n]*`)

// TestReleaseVerifiesWhatItPublishes is T-R95: release.yml checks the object type of the tag, every
// cosign and gh attestation command names ${IMAGE}@${DIGEST}, verify scans that reference with the
// thresholds of ci.yml and publish needs verify; each command of "Verifying a release" in the
// container guide names @sha256: and none names a tag alone; each verification of the workflow has
// its command in the guide; ADR 0018 no longer says that binaries are signed.
func TestReleaseVerifiesWhatItPublishes(t *testing.T) {
	release := read(t, ".github/workflows/release.yml")
	ci := read(t, ".github/workflows/ci.yml")

	t.Run("the tag is annotated", func(t *testing.T) {
		job := jobBlock(t, "release.yml", release, "check-tag")
		// The API is used: the checkout of a tag push can leave a lightweight ref locally.
		if !regexp.MustCompile(`cat-file -t|%\(objecttype\)|git/ref/tags/\S*\s+--jq \.object\.type`).MatchString(job) {
			t.Error("check-tag does not test the object type of the tag (gh api .../git/ref/tags/$TAG --jq .object.type)")
		}
	})

	t.Run("every command names the digest", func(t *testing.T) {
		commands := verifyCommandRE.FindAllString(release, -1)
		if len(commands) < 4 {
			t.Errorf("%d cosign and gh attestation commands in release.yml", len(commands))
		}
		for _, c := range commands {
			if !strings.Contains(c, "${IMAGE}@${DIGEST}") {
				t.Errorf("a command that does not name ${IMAGE}@${DIGEST}: %s", c)
			}
		}
	})

	t.Run("verify scans the digest and publish needs verify", func(t *testing.T) {
		verify := jobBlock(t, "release.yml", release, "verify")
		scan := regexp.MustCompile(`(?s)uses: anchore/scan-action@[0-9a-f]{40}.*?(?:\n      - |\z)`).FindString(verify)
		if scan == "" {
			t.Fatal("the job verify has no scan step (anchore/scan-action)")
		}
		if !regexp.MustCompile(`image:[^\n]*IMAGE[^\n]*@[^\n]*DIGEST`).MatchString(scan) {
			t.Errorf("the scan of verify is not of ${IMAGE}@${DIGEST}:\n%s", scan)
		}
		ciScan := regexp.MustCompile(`(?s)uses: anchore/scan-action@[0-9a-f]{40}.*?(?:\n      - |\z)`).FindString(ci)
		if ciScan == "" {
			t.Fatal("ci.yml has no scan step")
		}
		for _, key := range []string{"fail-build", "severity-cutoff", "only-fixed"} {
			re := regexp.MustCompile(key + `:\s*(\S+)`)
			a, b := re.FindStringSubmatch(ciScan), re.FindStringSubmatch(scan)
			if a == nil || b == nil || a[1] != b[1] {
				t.Errorf("the scan of verify does not have the %s of the scan of ci.yml (%v and %v)", key, b, a)
			}
		}
		if idx := strings.Index(verify, "Verify the build provenance"); idx < 0 || strings.Index(verify, "anchore/scan-action") < idx {
			t.Error("the scan of verify does not come after the provenance check")
		}
		publish := jobBlock(t, "release.yml", release, "publish")
		if !regexp.MustCompile(`needs:\s*\[[^\]]*\bverify\b`).MatchString(publish) {
			t.Error("publish does not need verify")
		}
	})

	t.Run("the guide verifies by digest", func(t *testing.T) {
		guide := read(t, "docs/operations/container.md")
		i := strings.Index(guide, "\n## Verifying a release")
		if i < 0 {
			t.Fatal("the container guide has no section 'Verifying a release'")
		}
		section := guide[i+1:]
		if j := strings.Index(section[3:], "\n## "); j >= 0 {
			section = section[:j+3]
		}
		// A command can continue on the next lines with a backslash.
		section = regexp.MustCompile(`\\\n\s*`).ReplaceAllString(section, " ")
		got := map[string]bool{}
		for _, c := range verifyCommandRE.FindAllStringSubmatch(section, -1) {
			head := c[1]
			got[head] = true
			if !strings.Contains(c[0], "@sha256:") {
				t.Errorf("a command of the guide that does not name @sha256: %s", c[0])
			}
		}
		for _, c := range verifyCommandRE.FindAllStringSubmatch(release, -1) {
			if strings.HasPrefix(c[1], "cosign sign") || strings.HasPrefix(c[1], "cosign attest") {
				continue // what the pipeline does; the guide verifies
			}
			if !got[c[1]] {
				t.Errorf("the workflow runs %q and the guide has no such command", c[1])
			}
		}
	})

	t.Run("ADR 0018 does not claim binaries", func(t *testing.T) {
		adr := read(t, "docs/adr/0018-supply-chain.md")
		if strings.Contains(strings.ToLower(adr), "images and binaries") {
			t.Error("ADR 0018 still says that it signs images and binaries")
		}
		if !strings.Contains(adr, "0074") {
			t.Error("ADR 0018 does not point to ADR 0074, which amends it")
		}
	})
}

// patternOf returns the expression of a variable of redact.go that is a regexp.MustCompile of a
// raw string.
func patternOf(t *testing.T, source, name string) string {
	t.Helper()
	m := regexp.MustCompile(`\b` + name + `\s*=\s*regexp\.MustCompile\(` + "`([^`]*)`" + `\)`).FindStringSubmatch(source)
	if m == nil {
		t.Fatalf("internal/telemetry/redact.go has no %s = regexp.MustCompile(`...`)", name)
	}
	return m[1]
}

// TestSecretShapesAreScanned is T-R96: each pattern of a secret Pandokos issues in the redaction of
// the logs (key, session token, signing secret) is, character for character, the regex of a rule of
// .gitleaks.toml.
func TestSecretShapesAreScanned(t *testing.T) {
	source := read(t, "internal/telemetry/redact.go")
	gitleaks := read(t, ".gitleaks.toml")
	for variable, rule := range map[string]string{"keyPattern": "pandokos-key", "sessionPattern": "pandokos-session", "whsecPattern": "pandokos-signing-secret"} {
		pattern := patternOf(t, source, variable)
		m := regexp.MustCompile(`(?s)\[\[rules\]\]\s*id = "` + regexp.QuoteMeta(rule) + `"(.*?)(?:\n\[\[|\z)`).FindStringSubmatch(gitleaks)
		if m == nil {
			t.Errorf(".gitleaks.toml has no rule %s for the pattern %s", rule, variable)
			continue
		}
		re := regexp.MustCompile(`(?m)^regex\s*=\s*'''(.*)'''\s*$`).FindStringSubmatch(m[1])
		if re == nil {
			t.Errorf("the rule %s has no regex = '''...'''", rule)
			continue
		}
		if re[1] != pattern {
			t.Errorf("the rule %s is %q, the pattern %s of redact.go is %q", rule, re[1], variable, pattern)
		}
	}
}

// TestScheduledChecks is T-R97: scheduled.yml has a schedule trigger, the jobs fuzz, vuln and image
// and (unless the check of the rulesets moved to the release check list of CONTRIBUTING.md because
// the token cannot read them) settings; every job is conditional on the repository, no job has
// continue-on-error, nothing has a permission beyond contents: read; the govulncheck version equals
// the Makefile's.
func TestScheduledChecks(t *testing.T) {
	text := read(t, ".github/workflows/scheduled.yml")
	if !regexp.MustCompile(`(?m)^  schedule:\s*$`).MatchString(text) || !regexp.MustCompile(`(?m)^\s+- cron: `).MatchString(text) {
		t.Error("scheduled.yml has no schedule trigger with a cron")
	}
	if !strings.Contains(text, "workflow_dispatch") {
		t.Error("scheduled.yml cannot be dispatched by hand")
	}
	jobs := []string{"fuzz", "vuln", "image"}
	if hasJob(text, "settings") {
		jobs = append(jobs, "settings")
	} else if !regexp.MustCompile(`(?is)release.{0,40}check.{0,400}ruleset`).MatchString(read(t, "CONTRIBUTING.md")) {
		t.Error("scheduled.yml has no job settings and the release check list of CONTRIBUTING.md does not name the rulesets")
	}
	for _, name := range jobs {
		job := jobBlock(t, "scheduled.yml", text, name)
		if !strings.Contains(job, "github.repository == 'pandokos/pandokos'") {
			t.Errorf("the job %s is not conditional on the repository", name)
		}
		if strings.Contains(job, "continue-on-error") {
			t.Errorf("the job %s has continue-on-error", name)
		}
	}
	if strings.Contains(text, "continue-on-error") {
		t.Error("scheduled.yml has continue-on-error")
	}
	if !regexp.MustCompile(`(?m)^permissions:\s*\n  contents: read\s*$`).MatchString(text) {
		t.Error("scheduled.yml has no top-level permissions: contents: read")
	}
	for _, m := range regexp.MustCompile(`(?m)^\s+([a-z-]+): (write|admin)\b`).FindAllStringSubmatch(text, -1) {
		t.Errorf("scheduled.yml grants %s: %s", m[1], m[2])
	}
	for _, m := range regexp.MustCompile(`(?m)^\s+permissions:\s*\n((?:\s+[a-z-]+: \w+\n)+)`).FindAllStringSubmatch(text, -1) {
		for _, l := range strings.Split(strings.TrimSpace(m[1]), "\n") {
			if strings.TrimSpace(l) != "contents: read" {
				t.Errorf("scheduled.yml has a permission beyond contents: read: %s", strings.TrimSpace(l))
			}
		}
	}
	want := regexp.MustCompile(`GOVULNCHECK := \S+@(v\S+)`).FindStringSubmatch(read(t, "Makefile"))
	if want == nil {
		t.Fatal("the Makefile has no GOVULNCHECK := module@version")
	}
	got := regexp.MustCompile(`govulncheck@(v[0-9][^\s]*)`).FindAllStringSubmatch(text, -1)
	// make vuln takes the version of the Makefile itself.
	if len(got) == 0 && !regexp.MustCompile(`(?m)run: make vuln\s*$`).MatchString(text) {
		t.Error("scheduled.yml does not run govulncheck by version")
	}
	for _, g := range got {
		if g[1] != want[1] {
			t.Errorf("scheduled.yml runs govulncheck %s, the Makefile %s", g[1], want[1])
		}
	}
}
