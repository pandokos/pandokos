package genaiprices

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// MaxDatasetSize is the largest dataset Parse accepts, in bytes.
const MaxDatasetSize = 16 << 20

// Errors of Parse. None quotes the document.
var (
	// ErrTooLarge is returned for a dataset above MaxDatasetSize.
	ErrTooLarge = errors.New("genaiprices: the dataset is larger than 16 MiB")
	// ErrInvalid is returned for a document that is not a price dataset of format v2.
	ErrInvalid = errors.New("genaiprices: the document is not a valid genai-prices dataset (format v2)")
)

// Provider is a provider of the dataset.
type Provider struct {
	ID     string  `json:"id"`
	Models []Model `json:"models"`
}

// Model is a model of the dataset. Prices is decoded lazily by Extract.
type Model struct {
	ID            string          `json:"id"`
	Match         Clause          `json:"match"`
	Prices        json.RawMessage `json:"prices"`
	PriceVariants []Variant       `json:"price_variants"`
	ContextWindow *json.Number    `json:"context_window"`
	Deprecated    bool            `json:"deprecated"`
}

// Variant is a price variant of a model (a service tier, a processing mode); the importer
// ignores them and reports their keys.
type Variant struct {
	When map[string]json.RawMessage `json:"when"`
}

// Dataset is a parsed price dataset.
type Dataset struct {
	providers []Provider
	issues    []Issue
}

// Parse reads a dataset of format v2: a JSON array of providers. Numbers are kept as text,
// members it does not use are ignored and a wrong type in a member it uses is an error. The
// error never quotes the document. Regular expressions are compiled once; a pattern RE2
// refuses matches nothing and is reported by Issues.
func Parse(b []byte) (*Dataset, error) {
	if len(b) > MaxDatasetSize {
		return nil, ErrTooLarge
	}
	if t := bytes.TrimLeft(b, " \t\r\n"); len(t) == 0 || t[0] != '[' {
		return nil, ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var providers []Provider
	if err := dec.Decode(&providers); err != nil {
		return nil, ErrInvalid
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalid
	}
	d := &Dataset{providers: providers}
	cache := map[string]*compiled{}
	for i := range d.providers {
		p := &d.providers[i]
		for j := range p.Models {
			m := &p.Models[j]
			if m.Match.prepare(cache) {
				d.issues = append(d.issues, Issue{Reason: ReasonRegexUnsupported, Detail: p.ID + "/" + m.ID})
			}
		}
	}
	return d, nil
}

// Find returns the first model of the dataset provider whose match accepts upstreamID.
func (d *Dataset) Find(datasetProvider, upstreamID string) (*Model, bool) {
	for i := range d.providers {
		p := &d.providers[i]
		if p.ID != datasetProvider {
			continue
		}
		for j := range p.Models {
			if p.Models[j].Match.matches(upstreamID) {
				return &p.Models[j], true
			}
		}
	}
	return nil, false
}

// Issues returns the problems of the dataset as a whole: one regex_unsupported issue per model
// whose pattern RE2 refuses, naming "provider/model", never the pattern.
func (d *Dataset) Issues() []Issue {
	return append([]Issue(nil), d.issues...)
}
