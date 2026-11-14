package fixtures

import "embed"

//go:embed openai/* anthropic/* compat/*
var files embed.FS

// Read returns the content of the fixture openai/<name> and panics if it does not exist.
func Read(name string) []byte { return read("openai/" + name) }

// ReadAnthropic returns the content of the fixture anthropic/<name> (the Messages API) and panics
// if it does not exist.
func ReadAnthropic(name string) []byte { return read("anthropic/" + name) }

// ReadCompat returns the content of the fixture compat/<name> (the wire of OpenAI-compatible
// servers) and panics if it does not exist.
func ReadCompat(name string) []byte { return read("compat/" + name) }

func read(path string) []byte {
	b, err := files.ReadFile(path)
	if err != nil {
		panic("fixtures: " + err.Error())
	}
	return b
}

// Dir returns the names of the files of the fixture directory dir ("openai", "anthropic" or
// "compat"), sorted.
func Dir(dir string) []string {
	entries, err := files.ReadDir(dir)
	if err != nil {
		panic("fixtures: " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
