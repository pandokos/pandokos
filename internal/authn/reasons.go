package authn

// The reasons of an authentication failure (Failure.Reason). They are the closed list behind the
// pandokos.authn.reason attribute of the metrics and the reason field of the failure lines: no
// other value is used.
const (
	ReasonMalformed              = "malformed"
	ReasonKindNotAccepted        = "kind_not_accepted"
	ReasonUnknownKey             = "unknown_key"
	ReasonWrongSecret            = "wrong_secret"
	ReasonRevoked                = "revoked"
	ReasonExpired                = "expired"
	ReasonServiceAccountDisabled = "service_account_disabled"
	ReasonServiceAccountNotValid = "service_account_not_valid"
	ReasonAlgorithmNotAllowed    = "algorithm_not_allowed"
	ReasonUnknownSigningKey      = "unknown_signing_key"
	ReasonBadSignature           = "bad_signature"
	ReasonIssuerMismatch         = "issuer_mismatch"
	ReasonWrongAudience          = "wrong_audience"
	ReasonNotYetValid            = "not_yet_valid"
	ReasonLifetimeTooLong        = "lifetime_too_long"
	ReasonIssuerDisabled         = "issuer_disabled"
	ReasonRequiredClaimMissing   = "required_claim_missing"
	ReasonSubjectMissing         = "subject_missing"
	ReasonTenantClaimMissing     = "tenant_claim_missing"
	ReasonUnknownIssuer          = "unknown_issuer"
	ReasonAmbiguousAudience      = "ambiguous_audience"
	ReasonTenantNotListed        = "tenant_not_listed"
	ReasonSubjectNotMapped       = "subject_not_mapped"
)

// Reasons lists every failure reason, in a fixed order.
var Reasons = []string{
	ReasonMalformed,
	ReasonKindNotAccepted,
	ReasonUnknownKey,
	ReasonWrongSecret,
	ReasonRevoked,
	ReasonExpired,
	ReasonServiceAccountDisabled,
	ReasonServiceAccountNotValid,
	ReasonAlgorithmNotAllowed,
	ReasonUnknownSigningKey,
	ReasonBadSignature,
	ReasonIssuerMismatch,
	ReasonWrongAudience,
	ReasonNotYetValid,
	ReasonLifetimeTooLong,
	ReasonIssuerDisabled,
	ReasonRequiredClaimMissing,
	ReasonSubjectMissing,
	ReasonTenantClaimMissing,
	ReasonUnknownIssuer,
	ReasonAmbiguousAudience,
	ReasonTenantNotListed,
	ReasonSubjectNotMapped,
}
