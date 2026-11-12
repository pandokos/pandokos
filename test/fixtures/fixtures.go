package fixtures

import "embed"

//go:embed openai/*
var files embed.FS

// Read returns the content of the fixture openai/<name> and panics if it does not exist.
func Read(name string) []byte {
	b, err := files.ReadFile("openai/" + name)
	if err != nil {
		panic("fixtures: " + err.Error())
	}
	return b
}
