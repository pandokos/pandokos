//go:build drill

// Package drill is the restore drill (sprint 12.3, build tag drill, make test-drill): a suite that
// executes docs/operations/backup-and-restore.md step by step against PostgreSQL servers and a key
// service that it starts, destroys and restores, inside the Compose project it created and nowhere
// else.
//
// What this file holds is the vocabulary the guide and the suite share: the step identifiers (held
// equal to the guide's anchors by test/repo T-R100) and the command the suite runs for a step, and
// the Compose project of a case. The world of a case (what it installs, backs up and destroys) is
// in world.go; the report in report.go.
package drill

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Step is a step of the guide, named by the identifier of its anchor (<a id="step-4-2"></a> for
// "4.2"): section, then the number or the letter; an s marks the column of a key service.
type Step string

// The steps of the backup guide, each carried by the guide as an anchor <a id="step-4-2"></a>
// (T-R100 holds the two sets equal). 1.a and 1.b are the two backups of section 1; the rest are the
// steps of sections 4, 5 and 6.
const (
	Step1a Step = "1.a" // the logical backup: pg_dump
	Step1b Step = "1.b" // the physical backup: pg_basebackup with WAL archiving

	Step41 Step = "4.1" // roles
	Step42 Step = "4.2" // the database
	Step43 Step = "4.3" // the schema
	Step44 Step = "4.4" // the key files
	Step45 Step = "4.5" // start serve
	Step46 Step = "4.6" // the checks
	Step47 Step = "4.7" // rotate nothing until they pass

	Step5a Step = "5.a" // request again the deletions completed after the backup
	Step5b Step = "5.b" // the platform's events list the deletions too
	Step5c Step = "5.c" // usage and audit rows after the backup are missing
	Step5d Step = "5.d" // sessions and keys revoked since the backup are valid again
	Step5e Step = "5.e" // a deletion that was running continues
	Step5f Step = "5.f" // events after the backup are lost (G-7)

	Step61  Step = "6.1"  // the backup and its key
	Step62  Step = "6.2"  // a backup older than a rotation, the old key kept
	Step63  Step = "6.3"  // the same backup, without the old key
	Step64  Step = "6.4"  // the key lost entirely
	Step61s Step = "6.1s" // the same four rows for a key service
	Step62s Step = "6.2s"
	Step63s Step = "6.3s"
	Step64s Step = "6.4s"
)

// Steps is every step, in the guide's order.
var Steps = []Step{Step1a, Step1b, Step41, Step42, Step43, Step44, Step45, Step46, Step47,
	Step5a, Step5b, Step5c, Step5d, Step5e, Step5f, Step61, Step62, Step63, Step64, Step61s, Step62s, Step63s, Step64s}

// The command the suite runs for a step, as the guide shows it under the step's anchor (T-R100
// compares the two). The name is Cmd and the identifier without its dot. A <placeholder> is
// replaced by the suite and kept as written in the guide. The exact options are settled by running
// them (plan C5); the constraints of the design are fixed: a superuser's dump in the custom format,
// one pg_restore of the whole archive with --exit-on-error into a database created empty after the
// roles, no --data-only, --disable-triggers, --no-owner or --no-acl.
const (
	Cmd1a = `pg_dump -U postgres -d <database> -Fc -f /backup/pandokos.dump`
	Cmd1b = `pg_basebackup -U postgres -D /backup/base -Ft -z -X none --checkpoint=fast`

	Cmd41 = `psql -U postgres -v ON_ERROR_STOP=1 -f deploy/postgres/roles.sql`
	Cmd42 = `psql -U postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE <database> OWNER pandokos_owner" -c "REVOKE ALL ON DATABASE <database> FROM PUBLIC" -c "GRANT CONNECT ON DATABASE <database> TO pandokos_gateway, pandokos_admin, pandokos_worker"` + "\n" +
		`pg_restore -U postgres -d <database> --exit-on-error /backup/pandokos.dump`
	Cmd43 = `pandokos migrate status`
	Cmd45 = `pandokos serve`
	Cmd46 = "pandokos audit export --tenant <tenant ID> --to-seq <seq kept with the head> --out tenant.ndjson\n" +
		"pandokos audit verify --tenant <tenant ID> --file tenant.ndjson --expect-head <hash kept outside>\n" +
		"pandokos platform-audit export --to-seq <seq kept with the head> --out platform.ndjson\n" +
		`pandokos platform-audit verify --file platform.ndjson --expect-head <hash kept outside>`

	Cmd5a = `pandokos tenant deletion list --state completed -o json`
	Cmd5b = `pandokos platform-event export --type io.pandokos.tenant.deleted.v1`

	Cmd63  = `psql -U postgres -d <database> -v kek_version=<version> -f deploy/postgres/recover-lost-kek.sql`
	Cmd63s = `psql -U postgres -d <database> -v kek_version=<transit-version> -f deploy/postgres/recover-lost-kek.sql`
	Cmd64  = `pandokos kek generate --out <new key file>`
)

// Services of compose.yaml, one profile each.
const (
	Source        = "source"
	Target        = "target"
	TargetNext    = "target-next"
	OpenBao       = "openbao"
	OpenBaoTarget = "openbao-target"
)

// Project is the Compose project of one case. It is started from test/drill/compose.yaml under a
// name the suite generates, the ports are the ones the system chose and read back, and everything
// is removed when the case ends, on failure too. It reads no address, name or password from the
// environment, and removes nothing but its own project.
type Project struct {
	Name    string
	Backup  string // the directory mounted in the servers as /backup: the case's t.TempDir
	Secrets string // the directory with postgres_password (the superuser's, generated)
	File    string
	Env     []string
}

// NewProject creates the directories of a case and registers the removal of its containers and
// volumes. It starts no service.
func NewProject(tb testing.TB) *Project {
	tb.Helper()
	root := tempDir(tb)
	p := &Project{Name: "pandokos-drill-" + randomHex(tb, 4), Backup: filepath.Join(root, "backup"), Secrets: filepath.Join(root, "secrets")}
	// The servers run as their own users: the backup directory must accept them. It lives in the
	// case's temporary directory and holds nothing but what the case makes.
	for dir, mode := range map[string]os.FileMode{p.Backup: 0o777, filepath.Join(p.Backup, "wal"): 0o777, p.Secrets: 0o755} {
		if err := os.MkdirAll(dir, mode); err != nil {
			tb.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			tb.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(p.Secrets, "postgres_password"), []byte(randomHex(tb, 16)), 0o644); err != nil { //nolint:gosec // read by the server's user in its container; a generated, case-local value
		tb.Fatal(err)
	}
	p.File = filepath.Join(repoRoot(tb), "test", "drill", "compose.yaml")
	p.Env = []string{"PANDOKOS_DRILL_BACKUP=" + p.Backup, "PANDOKOS_DRILL_SECRETS=" + p.Secrets}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		p.shareBackup(ctx) // the tools wrote as other users: the directory must be removable by the one who runs the tests
		_, _ = p.compose(ctx, "--profile", "*", "down", "-v", "--remove-orphans", "--timeout", "5")
	})
	return p
}

// tempDir is a directory of the case, removed when it ends. It is not tb.TempDir: that one carries the
// name of the test, which can hold a comma, and a list of files in the configuration is split at commas.
func tempDir(tb testing.TB) string {
	tb.Helper()
	dir, err := os.MkdirTemp("", "pandokos-drill-")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// SuperuserPassword is the password of the postgres role of this project's servers.
func (p *Project) SuperuserPassword(tb testing.TB) string {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join(p.Secrets, "postgres_password"))
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

func (p *Project) compose(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"compose", "-p", p.Name, "-f", p.File}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Env = append(os.Environ(), p.Env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// Up starts services and waits for their health check. Extra variables (the held target of the
// physical case) are given in env.
func (p *Project) Up(tb testing.TB, env []string, services ...string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	args := []string{}
	for _, s := range services {
		args = append(args, "--profile", s)
	}
	args = append(args, "up", "-d")
	if len(env) == 0 {
		args = append(args, "--wait")
	}
	args = append(args, services...)
	saved := p.Env
	p.Env = append(append([]string(nil), p.Env...), env...)
	defer func() { p.Env = saved }()
	if out, err := p.compose(ctx, args...); err != nil {
		tb.Fatalf("docker compose up %s: %v\n%s", strings.Join(services, " "), err, tail(out))
	}
}

// Port returns host:port of a service's published port.
func (p *Project) Port(tb testing.TB, service, port string) string {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := p.compose(ctx, "--profile", service, "port", service, port)
	if err != nil {
		tb.Fatalf("docker compose port %s %s: %v\n%s", service, port, err, tail(out))
	}
	return strings.TrimSpace(out)
}

// Exec runs a command in a service's container, as the container's default user unless user is set,
// with the given standard input, and returns what it printed (standard output and error together).
func (p *Project) Exec(ctx context.Context, service, user, stdin string, command ...string) (string, error) {
	args := []string{"--profile", service, "exec", "-T"}
	if user != "" {
		args = append(args, "-u", user)
	}
	args = append(args, service)
	args = append(args, command...)
	full := append([]string{"compose", "-p", p.Name, "-f", p.File}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Env = append(os.Environ(), p.Env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// Remove destroys a service and its volumes: the disaster of the case.
func (p *Project) Remove(tb testing.TB, service string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := p.compose(ctx, "--profile", service, "rm", "-s", "-f", "-v", service); err != nil {
		tb.Fatalf("docker compose rm %s: %v\n%s", service, err, tail(out))
	}
}

func tail(s string) string {
	if len(s) > 2000 {
		return "..." + s[len(s)-2000:]
	}
	return s
}

func randomHex(tb testing.TB, n int) string {
	tb.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func repoRoot(tb testing.TB) string {
	tb.Helper()
	dir, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && strings.Contains(string(b), "module github.com/pandokos/pandokos\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatal("repository root not found")
		}
		dir = parent
	}
}
