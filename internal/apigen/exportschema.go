package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// exportKind is one kind of the tenant data export: the entry of $defs that a line of its file
// validates against, the operation whose answer is the kind and whether the kind is the schema of
// the `items` of a page (a list) or of the answer itself (a stream of objects).
type exportKind struct {
	Name, Operation string
	Items           bool
}

// exportKinds is the table of internal/tenantexport.Kinds, with the operation that reads each
// kind. "settings" is not here: it is an object of the four settings reads (exportSettings).
var exportKinds = []exportKind{
	{"service_account", "listServiceAccounts", true},
	{"virtual_key", "listVirtualKeys", true},
	{"credential", "listCredentials", true},
	{"token_issuer", "listTokenIssuers", true},
	{"subject_mapping", "listSubjectMappings", true},
	{"platform_issuer", "listTenantPlatformIssuers", true},
	{"limit", "listLimits", true},
	{"platform_limit", "listTenantPlatformLimits", true},
	{"limit_event", "listLimitEvents", true},
	{"price_list_version", "listPriceListVersions", true},
	{"member", "listMembers", true},
	{"role", "listRoles", true},
	{"support_action", "listSupportActions", true},
	{"usage_record", "exportUsageRecords", false},
	{"audit_event", "exportAuditEvents", false},
}

// exportSettings maps each member of settings.json to the operation whose answer it is.
var exportSettings = []struct{ Member, Operation string }{
	{"time_zone", "getTenantTimeZone"},
	{"currency", "getActiveCurrency"},
	{"metadata_keys", "getMetadataKeys"},
	{"model_restriction", "getModelRestriction"},
}

// exportSchema returns the JSON Schema (draft 2020-12) of the export: the manifest at the root and
// one entry of $defs per kind, with every component schema they reach.
func (g *generator) exportSchema() ([]byte, error) {
	defs := map[string]any{}
	need := map[string]bool{}

	// body is the schema of the success answer of an operation, as a component name.
	body := func(opID string) (string, error) {
		for _, pi := range g.doc.Paths {
			for _, op := range []*operation{pi.Get, pi.Post, pi.Patch, pi.Put, pi.Delete} {
				if op == nil || op.OperationID != opID {
					continue
				}
				for _, code := range sortedKeys(op.Responses) {
					if !strings.HasPrefix(code, "2") {
						continue
					}
					rn, err := refName(op.Responses[code].Ref, "responses")
					if err != nil {
						return "", err
					}
					r := g.doc.Components.Responses[rn]
					for _, ct := range []string{"application/json", "application/x-ndjson"} {
						if m, ok := r.Content[ct]; ok && m.Schema != nil && m.Schema.Ref != "" {
							return refName(m.Schema.Ref, "schemas")
						}
					}
				}
				return "", fmt.Errorf("operation %s has no object answer", opID)
			}
		}
		return "", fmt.Errorf("unknown operation %s", opID)
	}

	for _, k := range exportKinds {
		name, err := body(k.Operation)
		if err != nil {
			return nil, err
		}
		var s *schema
		if k.Items {
			list := g.doc.Components.Schemas[name]
			if list == nil || list.Properties.By["items"] == nil || list.Properties.By["items"].Items == nil {
				return nil, fmt.Errorf("%s: the answer of %s has no items", k.Name, k.Operation)
			}
			s = list.Properties.By["items"].Items
		} else {
			s = &schema{Ref: "#/components/schemas/" + name}
		}
		defs[k.Name] = g.jsonSchema(s, need)
	}
	members := map[string]any{}
	var required []string
	for _, m := range exportSettings {
		name, err := body(m.Operation)
		if err != nil {
			return nil, err
		}
		members[m.Member] = g.jsonSchema(&schema{Ref: "#/components/schemas/" + name}, need)
		required = append(required, m.Member)
	}
	defs["settings"] = map[string]any{
		"type": "object", "description": "The tenant's settings: the answers of the four reads of the settings.",
		"additionalProperties": false, "required": required, "properties": members,
	}
	// The closure of the components reached.
	for changed := true; changed; {
		changed = false
		for name := range need {
			if _, done := defs[name]; done {
				continue
			}
			s, ok := g.doc.Components.Schemas[name]
			if !ok {
				return nil, fmt.Errorf("unknown schema %q", name)
			}
			defs[name] = g.jsonSchema(s, need)
			changed = true
		}
	}

	var kindNames []any
	for _, k := range exportKinds {
		kindNames = append(kindNames, k.Name)
	}
	kindNames = append([]any{"settings"}, kindNames...)
	root := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         "urn:pandokos:schema:tenant-export:v1",
		"title":       "Pandokos tenant data export, manifest",
		"description": "manifest.json of an export made by pandokos tenant-export. Each file of the export is validated against the entry of $defs that its kind names.",
		"type":        "object",
		"required":    []any{"format", "version", "tenant_id", "created_at", "pandokos_version", "files", "audit_head", "usage_from", "usage_to"},
		"properties": map[string]any{
			"format":           map[string]any{"const": "pandokos-tenant-export"},
			"version":          map[string]any{"const": 1},
			"tenant_id":        map[string]any{"type": "string", "format": "uuid"},
			"created_at":       map[string]any{"type": "string", "format": "date-time"},
			"pandokos_version": map[string]any{"type": "string"},
			"files": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "required": []any{"name", "kind", "records", "sha256"},
				"properties": map[string]any{
					"name":    map[string]any{"type": "string"},
					"kind":    map[string]any{"enum": kindNames, "description": "The entry of $defs that each line of the file validates against."},
					"records": map[string]any{"type": "integer", "minimum": 0},
					"sha256":  map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
				},
			}},
			"audit_head": map[string]any{"type": "object", "required": []any{"seq", "hash", "anchor_seq"}, "properties": map[string]any{
				"seq":        map[string]any{"type": "integer", "minimum": 0},
				"hash":       map[string]any{"type": "string"},
				"anchor_seq": map[string]any{"type": "integer", "minimum": 0},
			}},
			"usage_from": map[string]any{"type": "string", "format": "date-time"},
			"usage_to":   map[string]any{"type": "string", "format": "date-time"},
		},
		"$defs": defs,
	}
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// jsonSchema converts a schema of the document to its JSON Schema form: references point to $defs
// (and are recorded in need), vendor members are dropped.
func (g *generator) jsonSchema(s *schema, need map[string]bool) map[string]any {
	out := map[string]any{}
	if s.Ref != "" {
		name, _ := refName(s.Ref, "schemas")
		need[name] = true
		out["$ref"] = "#/$defs/" + name
	}
	switch {
	case s.Type.Name == "":
	case s.Type.Nullable:
		out["type"] = []any{s.Type.Name, "null"}
	default:
		out["type"] = s.Type.Name
	}
	set := func(k string, v any) { out[k] = v }
	if s.Format != "" {
		set("format", s.Format)
	}
	if s.Description != "" {
		set("description", strings.TrimSpace(s.Description))
	}
	if s.Pattern != "" {
		set("pattern", s.Pattern)
	}
	if len(s.Enum) > 0 {
		e := make([]any, len(s.Enum))
		for i, v := range s.Enum {
			e[i] = v
		}
		set("enum", e)
	}
	if s.Default != nil {
		switch s.Type.Name {
		case "boolean":
			set("default", *s.Default == "true")
		case "integer", "number":
			if n, err := strconv.ParseFloat(*s.Default, 64); err == nil {
				set("default", n)
			}
		default:
			set("default", *s.Default)
		}
	}
	for k, v := range map[string]*int{"minLength": s.MinLength, "maxLength": s.MaxLength, "minimum": s.Minimum, "maximum": s.Maximum, "minItems": s.MinItems, "maxItems": s.MaxItems, "maxProperties": s.MaxProperties} {
		if v != nil {
			set(k, *v)
		}
	}
	if s.UniqueItems {
		set("uniqueItems", true)
	}
	if s.Items != nil {
		set("items", g.jsonSchema(s.Items, need))
	}
	if len(s.Properties.Names) > 0 {
		props := map[string]any{}
		for _, n := range s.Properties.Names {
			props[n] = g.jsonSchema(s.Properties.By[n], need)
		}
		set("properties", props)
	}
	if len(s.Required) > 0 {
		r := make([]any, len(s.Required))
		for i, v := range s.Required {
			r[i] = v
		}
		set("required", r)
	}
	switch {
	case s.AdditionalProperties.Closed != nil:
		set("additionalProperties", *s.AdditionalProperties.Closed)
	case s.AdditionalProperties.Values != nil:
		set("additionalProperties", g.jsonSchema(s.AdditionalProperties.Values, need))
	}
	if s.RawJSON {
		// A raw JSON object is any object.
		delete(out, "properties")
		delete(out, "additionalProperties")
	}
	return out
}

func exportSchemaOf(path string) ([]byte, error) {
	g, err := load(path)
	if err != nil {
		return nil, err
	}
	if g.doc.Components.Schemas == nil {
		return nil, errors.New("no schemas")
	}
	return g.exportSchema()
}
