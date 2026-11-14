package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
)

// Sprint 2.1, section 5.4: the credential operations of a tenant. The operations of the
// platform (tenant_credentials.go) share the functions below; they never seal a secret.
//
// Rules that hold for every function here:
//   - the secret of a request is a secret.Secret from the moment it is read, and is never put in
//     an error, a log line, a problem or an audit row;
//   - the provider is asked before the transaction is opened;
//   - the audit row is written in the transaction that changes the data;
//   - only the metadata view and metadata columns are read: the admin role cannot read the
//     sealed secret.

const (
	minSecretBytes = 8
	maxSecretBytes = 8192
	changedByTen   = "tenant"
	changedByPlat  = "platform"
)

// checkProvider refuses a provider that is not one of vault.Providers.
func checkProvider(name string) *adminapi.FieldError {
	if !vault.KnownProvider(name) {
		return &adminapi.FieldError{Field: "provider", Message: "must name a provider this installation supports"}
	}
	return nil
}

// checkSecret applies the rules of the secret: 8 to 8192 bytes, no control character, no white
// space at either end. A message names the member and the rule, never the value.
func checkSecret(v string) *adminapi.FieldError {
	switch {
	case len(v) < minSecretBytes:
		return &adminapi.FieldError{Field: "secret", Message: "length must be at least 8 bytes"}
	case len(v) > maxSecretBytes:
		return &adminapi.FieldError{Field: "secret", Message: "length must be at most 8192 bytes"}
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return &adminapi.FieldError{Field: "secret", Message: "must not contain control characters"}
		}
	}
	if strings.TrimSpace(v) != v {
		return &adminapi.FieldError{Field: "secret", Message: "must not start or end with white space"}
	}
	return nil
}

func fieldsError(fs ...*adminapi.FieldError) error {
	var out []adminapi.FieldError
	for _, f := range fs {
		if f != nil {
			out = append(out, *f)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return &invalidError{fields: out}
}

// validatorFor returns the validator of a provider when the request asks for validation.
func (s *server) validatorFor(provider string, validate bool) (vault.Validator, *adminapi.FieldError) {
	if !validate {
		return nil, nil
	}
	v, ok := s.validators[provider]
	if !ok || v == nil {
		return nil, &adminapi.FieldError{Field: "validate", Message: "is not available for this provider"}
	}
	return v, nil
}

// validate presents the secret to the provider. It runs before any transaction is opened and
// logs one line with the outcome and the status; the response of the provider is never seen.
func (s *server) validate(ctx context.Context, tid tenant.ID, provider string, v vault.Validator, sec secret.Secret) error {
	start := time.Now()
	verdict, status := v.Validate(ctx, sec)
	outcome := "unavailable"
	switch verdict {
	case vault.Accepted:
		outcome = "accepted"
	case vault.Rejected:
		outcome = "rejected"
	}
	s.log.Info("credential validation", "request_id", stateOf(ctx).id, "tenant_id", tid.String(), "provider", provider,
		"outcome", outcome, "upstream_status", status, "duration_ms", time.Since(start).Milliseconds())
	switch verdict {
	case vault.Accepted:
		return nil
	case vault.Rejected:
		return errCredentialRejected
	}
	return errValidationUnavailable
}

// inTenant runs fn in a session of the tenant. For a platform operation it first verifies that
// the tenant exists.
func (s *server) inTenant(ctx context.Context, tid tenant.ID, platform bool, fn func(ctx context.Context, tx store.Tx, q *queries.Queries) error) error {
	return s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if platform {
			if _, err := q.GetTenant(ctx, tid.String()); err != nil {
				if isNoRowsErr(err) {
					return errNotFound
				}
				return err
			}
		}
		return fn(ctx, tx, q)
	})
}

// recordDataKey audits the creation of a tenant's data key, when this write created it.
func (s *server) recordDataKey(ctx context.Context, tx store.Tx, tid tenant.ID, sealed vault.Sealed) error {
	if !sealed.DataKeyCreated {
		return nil
	}
	return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(principalOf(ctx)), Action: "data_key.create", ResourceType: "data_key", ResourceID: "1",
		Outcome: "success", RequestID: stateOf(ctx).id, Detail: map[string]any{"version": sealed.KeyVersion, "kek_version": sealed.KEKVersion}})
}

// recordCredential writes the audit row of an operation on a credential.
func recordCredential(ctx context.Context, tx store.Tx, tid tenant.ID, action, id, reason string, detail map[string]any) error {
	return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(principalOf(ctx)), Action: action, ResourceType: "credential", ResourceID: id,
		Outcome: "success", RequestID: stateOf(ctx).id, Reason: reason, Detail: detail})
}

// identity is the part of the detail that tells which credential a row is about.
func identity(c queries.LockCredentialRow) map[string]any {
	return map[string]any{"provider": c.Provider, "label": c.Label, "fingerprint": c.Fingerprint}
}

// listQuery is what the two list operations have in common.
type listQuery struct {
	sort            adminapi.Sort
	cursor          string
	limit           int
	state, provider string
}

func (s *server) listCredentials(ctx context.Context, tid tenant.ID, platform bool, lq listQuery) (*adminapi.CredentialList, error) {
	sortBy := string(lq.sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, _, id, err := decodeCursor(lq.cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(lq.limit)
	var rows []queries.PandokosProviderCredentialMetadatum
	err = s.inTenant(ctx, tid, platform, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		rows, err = q.ListCredentials(ctx, queries.ListCredentialsParams{
			State: lq.state, Provider: lq.provider, After: after, Sort: sortBy, AfterID: afterID(after, id), PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.CredentialList{Items: []adminapi.Credential{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor(sortBy, "", rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, credentialOut(r))
	}
	return out, nil
}

func (s *server) getCredential(ctx context.Context, tid tenant.ID, platform bool, id string) (*adminapi.Credential, error) {
	var row credRow
	err := s.inTenant(ctx, tid, platform, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		row, err = q.GetCredential(ctx, id)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	o := credentialOut(row)
	return &o, nil
}

// ListCredentials lists the credentials of a tenant.
func (s *server) ListCredentials(ctx context.Context, in adminapi.ListCredentialsInput) (*adminapi.CredentialList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	return s.listCredentials(ctx, tid, false, listQuery{in.Sort, in.Cursor, in.Limit, string(in.State), in.Provider})
}

// GetCredential reads the metadata of a credential.
func (s *server) GetCredential(ctx context.Context, in adminapi.GetCredentialInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	return s.getCredential(ctx, tid, false, in.Credential)
}

// CreateCredential registers a provider credential: the secret is sealed under the data key of
// the tenant and the first credential of a provider becomes its default.
func (s *server) CreateCredential(ctx context.Context, in adminapi.CreateCredentialInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	v, verr := s.validatorFor(b.Provider, b.ValidateAtWrite)
	if err := fieldsError(checkProvider(b.Provider), checkSecret(b.Secret), verr); err != nil {
		return nil, err
	}
	sec := secret.New(b.Secret)
	if v != nil {
		if err := s.validate(ctx, tid, b.Provider, v, sec); err != nil {
			return nil, err
		}
	}
	var out credRow
	for attempt := 0; ; attempt++ {
		err = s.inTenant(ctx, tid, false, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
			id, err := q.NewCredentialID(ctx)
			if err != nil {
				return err
			}
			sealed, err := s.sealer.Seal(ctx, tx, tid, id, sec)
			if err != nil {
				return err
			}
			row, err := q.CreateCredential(ctx, queries.CreateCredentialParams{
				TenantID: tid.String(), ID: id, Provider: b.Provider, Label: b.Label, Description: b.Description,
				KeyVersion: sealed.KeyVersion, Secret: sealed.Secret, Fingerprint: sealed.Fingerprint, Hint: sealed.Hint,
				ExpiresAt: utcPtr(b.ExpiresAt), Validated: v != nil,
			})
			if err != nil {
				return err
			}
			out = credRow(row)
			if err := s.recordDataKey(ctx, tx, tid, sealed); err != nil {
				return err
			}
			detail := map[string]any{"provider": row.Provider, "label": row.Label, "fingerprint": row.Fingerprint, "hint": row.Hint,
				"is_default": row.IsDefault, "validated": row.ValidatedAt != nil}
			if row.ExpiresAt != nil {
				detail["expires_at"] = fmtTime(row.ExpiresAt)
			}
			return recordCredential(ctx, tx, tid, "credential.create", row.ID, "", detail)
		})
		if kind, name, ok := store.ConstraintViolation(err); ok && kind == "unique" {
			switch {
			case name == "provider_credential_label_key":
				return nil, errConflict
			case name == "provider_credential_default_key" && attempt == 0:
				continue // two first credentials of a provider at once: the loser retries as a non-default
			}
		}
		if err != nil {
			return nil, err
		}
		o := credentialOut(out)
		return &o, nil
	}
}

// peekUpdate records, before the body is decoded, whether an update names the description. The
// generated type cannot tell an absent description from an empty one, and an empty one clears.
func peekUpdate(r *http.Request, st *reqState) {
	if r.Body == nil {
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil {
		return
	}
	if v, ok := members["description"]; ok && len(v) > 0 && v[0] == '"' {
		st.descriptionGiven = true
	}
}

// UpdateCredential changes the label, the description and the expiry.
func (s *server) UpdateCredential(ctx context.Context, in adminapi.UpdateCredentialInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	setDescription := b.Description != "" || stateOf(ctx).descriptionGiven
	if b.Label == "" && !setDescription && !b.ExpiresAt.Set {
		return nil, invalid("body", "must change at least one member")
	}
	var out credRow
	err = s.inTenant(ctx, tid, false, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		if _, err := q.LockCredential(ctx, in.Credential); err != nil {
			if isNoRowsErr(err) {
				return errNotFound
			}
			return err
		}
		cur, err := q.GetCredential(ctx, in.Credential)
		if err != nil {
			return err
		}
		detail := map[string]any{}
		par := queries.UpdateCredentialParams{ID: cur.ID}
		if b.Label != "" && b.Label != cur.Label {
			par.SetLabel, par.Label = true, b.Label
			detail["label"] = change(cur.Label, b.Label)
		}
		if setDescription && b.Description != cur.Description {
			par.SetDescription, par.Description = true, b.Description
			detail["description"] = change(cur.Description, b.Description)
		}
		if b.ExpiresAt.Set {
			var want *time.Time
			if !b.ExpiresAt.Null {
				u := b.ExpiresAt.Value.UTC()
				want = &u
			}
			if !sameTime(want, cur.ExpiresAt) {
				par.SetExpiresAt, par.ExpiresAt = true, want
				detail["expires_at"] = change(fmtTime(cur.ExpiresAt), fmtTime(want))
			}
		}
		if len(detail) == 0 {
			out = cur
			return nil
		}
		row, err := q.UpdateCredential(ctx, par)
		if err != nil {
			return err
		}
		out = credRow(row)
		return recordCredential(ctx, tx, tid, "credential.update", cur.ID, "", detail)
	})
	if kind, name, ok := store.ConstraintViolation(err); ok && kind == "unique" && name == "provider_credential_label_key" {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	o := credentialOut(out)
	return &o, nil
}

// ReplaceCredentialSecret seals a new secret for an existing credential. The ID of the
// credential, and so the pins of the keys, stay.
func (s *server) ReplaceCredentialSecret(ctx context.Context, in adminapi.ReplaceCredentialSecretInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	if err := fieldsError(checkSecret(b.Secret)); err != nil {
		return nil, err
	}
	// The provider of the credential picks the validator: a short read, no transaction stays open.
	var cur credRow
	if err := s.inTenant(ctx, tid, false, func(ctx context.Context, _ store.Tx, q *queries.Queries) error {
		var err error
		cur, err = q.GetCredential(ctx, in.Credential)
		return err
	}); err != nil {
		if isNoRowsErr(err) {
			return nil, errNotFound
		}
		return nil, err
	}
	v, verr := s.validatorFor(cur.Provider, b.ValidateAtWrite)
	if err := fieldsError(verr); err != nil {
		return nil, err
	}
	if cur.State == "revoked" {
		return nil, errInvalidState
	}
	sec := secret.New(b.Secret)
	if v != nil {
		if err := s.validate(ctx, tid, cur.Provider, v, sec); err != nil {
			return nil, err
		}
	}
	var out credRow
	err = s.inTenant(ctx, tid, false, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		lock, err := q.LockCredential(ctx, in.Credential)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if lock.State == "revoked" {
			return errInvalidState
		}
		sealed, err := s.sealer.Seal(ctx, tx, tid, lock.ID, sec)
		if err != nil {
			return err
		}
		row, err := q.ReplaceCredentialSecret(ctx, queries.ReplaceCredentialSecretParams{
			KeyVersion: sealed.KeyVersion, Secret: sealed.Secret, Fingerprint: sealed.Fingerprint, Hint: sealed.Hint,
			ExpiresAt: utcPtr(b.ExpiresAt), Validated: v != nil, ID: lock.ID,
		})
		if err != nil {
			return err
		}
		out = credRow(row)
		if err := s.recordDataKey(ctx, tx, tid, sealed); err != nil {
			return err
		}
		return recordCredential(ctx, tx, tid, "credential.rotate", lock.ID, "", map[string]any{
			"provider": lock.Provider, "label": lock.Label, "fingerprint": change(lock.Fingerprint, row.Fingerprint),
			"hint": row.Hint, "validated": row.ValidatedAt != nil,
		})
	})
	if err != nil {
		return nil, err
	}
	o := credentialOut(out)
	return &o, nil
}

// changeState is the state machine of section 5.4, shared by the tenant verbs and the two
// platform ones. The row is locked first; an operation that changes nothing writes no audit row.
func (s *server) changeState(ctx context.Context, tid tenant.ID, platform bool, id, verb, reason string) (*adminapi.Credential, error) {
	changedBy := changedByTen
	if platform {
		changedBy = changedByPlat
	}
	var out credRow
	err := s.inTenant(ctx, tid, platform, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		lock, err := q.LockCredential(ctx, id)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		var to string
		switch verb {
		case "disable":
			to = "disabled"
		case "enable":
			to = "active"
		default:
			to = "revoked"
		}
		switch lock.State {
		case to:
			// Nothing to change.
		case "revoked":
			return errInvalidState
		default:
			row, err := q.SetCredentialState(ctx, queries.SetCredentialStateParams{State: to, ChangedBy: changedBy, Reason: reason, ID: lock.ID})
			if err != nil {
				return err
			}
			out = credRow(row)
			return recordCredential(ctx, tx, tid, "credential."+verb, lock.ID, reason, identity(lock))
		}
		out, err = q.GetCredential(ctx, lock.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	o := credentialOut(out)
	return &o, nil
}

// DisableCredential disables a credential: it stays, it is not used.
func (s *server) DisableCredential(ctx context.Context, in adminapi.DisableCredentialInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	return s.changeState(ctx, tid, false, in.Credential, "disable", in.Body.Reason)
}

// EnableCredential enables a disabled credential, whoever disabled it.
func (s *server) EnableCredential(ctx context.Context, in adminapi.EnableCredentialInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	return s.changeState(ctx, tid, false, in.Credential, "enable", in.Body.Reason)
}

// RevokeCredential revokes a credential: final, and the sealed secret is destroyed.
func (s *server) RevokeCredential(ctx context.Context, in adminapi.RevokeCredentialInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	return s.changeState(ctx, tid, false, in.Credential, "revoke", in.Body.Reason)
}

// SetDefaultCredential makes a credential the default of its provider.
func (s *server) SetDefaultCredential(ctx context.Context, in adminapi.SetDefaultCredentialInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	var out credRow
	for attempt := 0; ; attempt++ {
		err = s.inTenant(ctx, tid, false, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
			lock, err := q.LockCredential(ctx, in.Credential)
			if isNoRowsErr(err) {
				return errNotFound
			}
			if err != nil {
				return err
			}
			if lock.State == "revoked" {
				return errInvalidState
			}
			if lock.IsDefault {
				out, err = q.GetCredential(ctx, lock.ID)
				return err
			}
			previous, err := q.ClearDefaultCredential(ctx, lock.Provider)
			if err != nil && !isNoRowsErr(err) {
				return err
			}
			row, err := q.SetDefaultCredential(ctx, lock.ID)
			if err != nil {
				return err
			}
			out = credRow(row)
			detail := identity(lock)
			delete(detail, "fingerprint")
			detail["previous"] = previous
			return recordCredential(ctx, tx, tid, "credential.set_default", lock.ID, in.Body.Reason, detail)
		})
		if kind, name, ok := store.ConstraintViolation(err); ok && kind == "unique" && name == "provider_credential_default_key" && attempt == 0 {
			continue // another default was set at the same moment
		}
		if err != nil {
			return nil, err
		}
		o := credentialOut(out)
		return &o, nil
	}
}

// DeleteCredential removes a credential and returns its metadata as it was. It is refused
// while a key that can still be used is pinned to it, and while it is the default and another
// credential of the provider is not revoked.
func (s *server) DeleteCredential(ctx context.Context, in adminapi.DeleteCredentialInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	var out credRow
	err = s.inTenant(ctx, tid, false, func(ctx context.Context, tx store.Tx, q *queries.Queries) error {
		lock, err := q.LockCredential(ctx, in.Credential)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		usable, err := q.CountUsablePins(ctx, lock.ID)
		if err != nil {
			return err
		}
		if usable > 0 {
			return errInvalidState
		}
		if lock.IsDefault {
			others, err := q.CountOtherCredentials(ctx, queries.CountOtherCredentialsParams{Provider: lock.Provider, ID: lock.ID})
			if err != nil {
				return err
			}
			if others > 0 {
				return errInvalidState
			}
		}
		pins, err := countPins(ctx, q, lock.ID)
		if err != nil {
			return err
		}
		out, err = q.GetCredential(ctx, lock.ID)
		if err != nil {
			return err
		}
		n, err := q.DeleteCredential(ctx, lock.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotFound
		}
		detail := identity(lock)
		detail["state"] = out.State
		detail["cleared_pins"] = pins
		return recordCredential(ctx, tx, tid, "credential.delete", lock.ID, "", detail)
	})
	if err != nil {
		return nil, err
	}
	o := credentialOut(out)
	return &o, nil
}

// countPins counts the keys of the tenant pinned to a credential, whatever their state: the
// ones that are revoked or expired lose the pin when the credential is deleted. It pages
// through the keys of the tenant, which is rare enough for a deletion.
func countPins(ctx context.Context, q *queries.Queries, credentialID string) (int, error) {
	const page = 200
	n, after, afterID := 0, false, "00000000-0000-0000-0000-000000000000"
	for {
		rows, err := q.ListVirtualKeys(ctx, queries.ListVirtualKeysParams{
			ServiceAccountID: "00000000-0000-0000-0000-000000000000", After: after, Sort: "created_at", AfterID: afterID, PageSize: page,
		})
		if err != nil {
			return 0, err
		}
		for _, k := range rows {
			if k.CredentialID != nil && *k.CredentialID == credentialID {
				n++
			}
		}
		if len(rows) < page {
			return n, nil
		}
		after, afterID = true, rows[len(rows)-1].ID
	}
}
