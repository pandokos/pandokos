// Package audit writes audit events in the transaction of the change they describe (AUD-03).
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
)

const maxDetailBytes = 8192

// ActorType is the type of the actor of an event: "platform", "service_account", "anonymous", "host".
type ActorType string

// The actor types written by this sprint.
const (
	ActorPlatform       ActorType = "platform"
	ActorServiceAccount ActorType = "service_account"
	ActorAnonymous      ActorType = "anonymous"
	ActorHost           ActorType = "host"
)

// Actor is who did it.
type Actor struct {
	Type  ActorType
	ID    string // service account ID or key ID of the platform key; "" when unknown
	Name  string
	KeyID string // key ID of the credential used; "" for the host
}

// ActorOf returns the actor of a principal.
func ActorOf(p authz.Principal) Actor {
	t := ActorServiceAccount
	if p.Kind == authz.KindPlatform {
		t = ActorPlatform
	}
	return Actor{Type: t, ID: p.ID, Name: p.Name, KeyID: p.KeyID}
}

// Event is one audit record.
type Event struct {
	Actor        Actor
	Action       string // "tenant.create"
	ResourceType string // "tenant"
	ResourceID   string
	Outcome      string // "success" or "failure"
	RequestID    string
	Reason       string         // from the request body, when given
	Detail       map[string]any // redacted description of the change
}

func sensitive(key string) bool {
	k := strings.ToLower(key)
	return telemetry.IsSensitiveKey(k) || k == "secret_hash" || k == "hash"
}

func walk(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if sensitive(k) || walk(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if walk(e) {
				return true
			}
		}
	}
	return false
}

// marshalDetail encodes a detail as JSON text and refuses a sensitive key at any depth and a
// detail over 8192 bytes. A nil detail is "{}".
func marshalDetail(detail map[string]any) (string, error) {
	if detail == nil {
		return "{}", nil
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return "", errors.New("audit: the detail cannot be encoded")
	}
	if len(b) > maxDetailBytes {
		return "", errors.New("audit: the detail is too large")
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return "", errors.New("audit: the detail cannot be decoded")
	}
	if walk(generic) {
		return "", errors.New("audit: the detail holds a sensitive member")
	}
	return string(b), nil
}

// RecordTenant writes e to the audit log of the tenant whose session tx is.
func RecordTenant(ctx context.Context, tx store.Tx, id tenant.ID, e Event) error {
	detail, err := marshalDetail(e.Detail)
	if err != nil {
		return err
	}
	return queries.New(tx).InsertAuditEvent(ctx, queries.InsertAuditEventParams{
		TenantID: id.String(), ActorType: string(e.Actor.Type), ActorID: e.Actor.ID, ActorName: e.Actor.Name, KeyID: e.Actor.KeyID,
		Action: e.Action, ResourceType: e.ResourceType, ResourceID: e.ResourceID, Outcome: e.Outcome,
		RequestID: e.RequestID, Reason: e.Reason, Detail: detail,
	})
}

// RecordPlatform writes e to the platform audit log; tx is in platform scope.
func RecordPlatform(ctx context.Context, tx store.Tx, e Event) error {
	detail, err := marshalDetail(e.Detail)
	if err != nil {
		return err
	}
	return queries.New(tx).InsertPlatformAuditEvent(ctx, queries.InsertPlatformAuditEventParams{
		ActorType: string(e.Actor.Type), ActorID: e.Actor.ID, ActorName: e.Actor.Name, KeyID: e.Actor.KeyID,
		Action: e.Action, ResourceType: e.ResourceType, ResourceID: e.ResourceID, Outcome: e.Outcome,
		RequestID: e.RequestID, Reason: e.Reason, Detail: detail,
	})
}

// RecordAuthFailure writes an authentication failure of the inference listener to the audit
// log of the tenant whose session tx is, through the one function the gateway role may
// execute. keyID is the key ID that was presented, reason a lower-case word, requestID the
// 32-hex request ID.
func RecordAuthFailure(ctx context.Context, tx store.Tx, keyID, reason, requestID string) error {
	return queries.New(tx).RecordAuthFailure(ctx, queries.RecordAuthFailureParams{KeyID: keyID, Reason: reason, RequestID: requestID})
}
