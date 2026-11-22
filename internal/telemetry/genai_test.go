package telemetry_test

import (
	"reflect"
	"testing"

	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/semconv/v1.41.0/httpconv"

	"github.com/pandokos/pandokos/internal/telemetry"
)

// TestGenAIPin is T-898: the instruments M1, M3, M4 and M5 have the name, unit and description of the
// pinned package (the GenAI and HTTP conventions v1.41.0), and the meter's scope has the schema URL
// of that version, the product's scope name and the build version.
func TestGenAIPin(t *testing.T) {
	r := newRig(t, nil)
	snap := r.observe(streamRequest("model-a"))

	pinned := map[string][3]string{
		"M1": {httpconv.ServerRequestDuration{}.Name(), httpconv.ServerRequestDuration{}.Unit(), httpconv.ServerRequestDuration{}.Description()},
		"M3": {genaiconv.ClientOperationDuration{}.Name(), genaiconv.ClientOperationDuration{}.Unit(), genaiconv.ClientOperationDuration{}.Description()},
		"M4": {genaiconv.ClientOperationTimeToFirstChunk{}.Name(), genaiconv.ClientOperationTimeToFirstChunk{}.Unit(), genaiconv.ClientOperationTimeToFirstChunk{}.Description()},
		"M5": {genaiconv.ClientTokenUsage{}.Name(), genaiconv.ClientTokenUsage{}.Unit(), genaiconv.ClientTokenUsage{}.Description()},
	}
	byID := map[string]want{}
	for _, w := range wantCatalogue {
		byID[w.ID] = w
	}
	for id, p := range pinned {
		if byID[id].Name != p[0] || byID[id].Unit != p[1] {
			t.Fatalf("%s: the design table says %s/%s but the pinned package says %s/%s", id, byID[id].Name, byID[id].Unit, p[0], p[1])
		}
		s, ok := snap.Series[p[0]]
		if !ok {
			t.Errorf("%s: %s is not exported", id, p[0])
			continue
		}
		if s.Unit != p[1] || s.Description != p[2] {
			t.Errorf("%s: exported unit %q description %q, want %q and %q of the pinned package", id, s.Unit, s.Description, p[1], p[2])
		}
	}

	if want := "https://opentelemetry.io/schemas/1.41.0"; semconv.SchemaURL != want {
		t.Fatalf("the pinned package has the schema URL %q", semconv.SchemaURL)
	}
	if snap.SchemaURL != semconv.SchemaURL {
		t.Errorf("the scope's schema URL is %q, want %q", snap.SchemaURL, semconv.SchemaURL)
	}
	if snap.Scope != scopeName {
		t.Errorf("the scope name is %q, want %q", snap.Scope, scopeName)
	}
	if snap.ScopeVersion != serviceVersion {
		t.Errorf("the scope version is %q, want the build version %q", snap.ScopeVersion, serviceVersion)
	}
	if !reflect.DeepEqual(snap.Resource["service.name"], "pandokos") {
		t.Errorf("service.name = %q", snap.Resource["service.name"])
	}
	_ = telemetry.Catalogue
}
