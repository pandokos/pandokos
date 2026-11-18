package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// The causes written to the audit rows of the rules that go with their subject (sprint 5.3,
// section 5.3).
const (
	causeKeyRevoked         = "key_revoked"
	causeMappingDeleted     = "subject_mapping_deleted"
	causeTokenIssuerDeleted = "token_issuer_deleted"
)

// subjectOfRules names the subject whose rules go: exactly one member is set.
type subjectOfRules struct {
	keyID     string // key ID of a virtual key
	mappingID string
	issuerID  string // a trusted issuer of the tenant
}

// deleteSubjectRules deletes the limit rules of a subject that is going away and writes one
// limit.delete row for each, in the log of the tenant of the transaction, as the principal of the
// request, with the reason of the request when there is one, every field of the rule (ruleDetail)
// and the cause. It returns the number of rules. The caller has taken LockLimitRules and removes
// or revokes the subject afterwards, in the same transaction: the foreign keys and the deferred
// trigger of migration 0011 refuse a path that does not. Nothing else deletes a rule here.
func deleteSubjectRules(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID, subj subjectOfRules, cause, causeID, reason string) (int, error) {
	rows, err := q.DeleteLimitRulesOfSubject(ctx, queries.DeleteLimitRulesOfSubjectParams{
		KeyID: subj.keyID, SubjectMappingID: nilIfEmpty(subj.mappingID), TrustedIssuerID: nilIfEmpty(subj.issuerID),
	})
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		detail := map[string]any{"subject_kind": r.SubjectKind, "subject_id": r.SubjectID, "provider": r.Provider, "model": r.Model,
			"cause": cause, "cause_id": causeID}
		ev := tenantEvent(ctx, "limit.delete", "limit_rule", r.ID,
			ruleDetail(detail, r.ProviderID, r.ModelID, r.Metric, r.LimitValue, r.Currency, r.Enforce, r.Thresholds))
		ev.Reason = reason
		if err := audit.RecordTenant(ctx, tx, tid, ev); err != nil {
			return 0, err
		}
	}
	if afterRuleDeletion != nil {
		if err := afterRuleDeletion(); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}
