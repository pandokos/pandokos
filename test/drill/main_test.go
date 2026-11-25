//go:build drill

package drill

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/pandokos/pandokos/test/instance"
)

// TestMain builds the binary once and fails, never skips, when Docker cannot be reached: the drill
// is a suite that needs it. When PANDOKOS_DRILL_REPORT names a file it writes the report of the
// run there; with CI set it also prints it as one line, as the last line of the log.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "the restore drill needs Docker and cannot reach it (docker info: %v)\n%s\n", err, out)
			return 1
		}
		if _, err := instance.TryBuild(); err != nil {
			fmt.Fprintln(os.Stderr, "building the binary:", err)
			return 1
		}
		code := m.Run()
		if path := os.Getenv("PANDOKOS_DRILL_REPORT"); path != "" {
			if _, err := WriteReport(path, false); err != nil {
				fmt.Fprintln(os.Stderr, "writing the report:", err)
				return 1
			}
		}
		if os.Getenv("CI") != "" {
			if b, err := WriteReport("", true); err == nil {
				fmt.Fprintln(os.Stdout, string(b))
			}
		}
		return code
	}())
}
