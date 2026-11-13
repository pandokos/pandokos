package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

const envPrefix = "PANDOKOS_"

// FileFromEnviron returns the value of PANDOKOS_CONFIG, or "".
func FileFromEnviron(environ []string) string {
	return parseEnviron(environ)["PANDOKOS_CONFIG"]
}

// parseEnviron returns the variables whose name starts with PANDOKOS_ and whose value is not
// empty; a later entry wins.
func parseEnviron(environ []string) map[string]string {
	out := map[string]string{}
	for _, e := range environ {
		name, value, ok := strings.Cut(e, "=")
		if !ok || !strings.HasPrefix(name, envPrefix) {
			continue
		}
		if value == "" {
			delete(out, name)
			continue
		}
		out[name] = value
	}
	return out
}

// Load builds the configuration from defaults, the file and the environment, in that order,
// validates all of it and reads the secret files. The error, if any, is a *ValidationError that
// lists every problem found.
func Load(src Source) (*Config, []Warning, error) {
	cfg := Defaults()
	var problems []Problem

	if src.File != "" {
		problems = append(problems, loadFile(cfg, src.File)...)
	}

	env := parseEnviron(src.Environ)
	known := map[string]bool{"PANDOKOS_CONFIG": true}
	for _, k := range keyTable {
		known[k.Env] = true
		if v, ok := env[k.Env]; ok {
			if err := k.set(cfg, v); err != nil {
				problems = append(problems, Problem{Key: k.Name, Message: err.Error()})
			}
		}
	}

	problems = append(problems, validate(cfg, src.Purpose)...)
	switch src.Purpose {
	case PurposeMigrate, PurposeInit:
		problems = append(problems, loadDatabaseSecrets(cfg, src.Purpose)...)
	default:
		problems = append(problems, loadSecrets(cfg)...)
		problems = append(problems, loadDatabaseSecrets(cfg, src.Purpose)...)
	}

	if len(problems) > 0 {
		return nil, nil, &ValidationError{Problems: problems}
	}
	return cfg, warnings(cfg, env, known), nil
}

func warnings(cfg *Config, env map[string]string, known map[string]bool) []Warning {
	ws := listenerAndURLWarnings(cfg)
	var unknown []string
	for name := range env {
		if known[name] || strings.HasPrefix(name, "PANDOKOS_CLI_") || strings.HasPrefix(name, "PANDOKOS_SERVICE_") || strings.HasPrefix(name, "PANDOKOS_PORT") || reServiceLink.MatchString(name) {
			continue
		}
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		ws = append(ws, Warning{Key: "environment", Message: "unknown variable " + name + " ignored"})
	}
	return ws
}

// reServiceLink matches the variables Kubernetes injects for a Service whose name ends in a
// word of ours (a Service "pandokos-database" gives PANDOKOS_DATABASE_SERVICE_HOST and so on).
var reServiceLink = regexp.MustCompile(`^PANDOKOS_[A-Z0-9_]*(_SERVICE_HOST|_SERVICE_PORT(_[A-Z0-9_]+)?|_PORT|_PORT_[0-9]+_(TCP|UDP)(_(PROTO|PORT|ADDR))?)$`)

var (
	reUnknownField = regexp.MustCompile(`^line (\d+): field (\S+) not found in type `)
	reLine         = regexp.MustCompile(`^line (\d+): `)
)

func loadFile(cfg *Config, path string) []Problem {
	b, err := os.ReadFile(path) //nolint:gosec // the path is operator configuration
	if err != nil {
		return []Problem{{Key: "file", Message: "cannot read the configuration file: " + err.Error()}}
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // an empty file is valid
		}
		return yamlProblems(err)
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return []Problem{{Key: "file", Message: "the file must contain one YAML document"}}
	}
	return nil
}

// yamlProblems turns decoder errors into problems that never quote a value: type-mismatch
// messages quote the offending value, which could be a secret typed in the wrong place.
func yamlProblems(err error) []Problem {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return []Problem{{Key: "file", Message: "invalid YAML"}}
	}
	var out []Problem
	for _, msg := range te.Errors {
		switch {
		case reUnknownField.MatchString(msg):
			m := reUnknownField.FindStringSubmatch(msg)
			out = append(out, Problem{Key: "file", Message: "line " + m[1] + ": unknown key " + m[2]})
		case reLine.MatchString(msg):
			m := reLine.FindStringSubmatch(msg)
			out = append(out, Problem{Key: "file", Message: "line " + m[1] + ": invalid value"})
		default:
			out = append(out, Problem{Key: "file", Message: "invalid YAML"})
		}
	}
	if len(out) == 0 {
		out = append(out, Problem{Key: "file", Message: "invalid YAML"})
	}
	return out
}
