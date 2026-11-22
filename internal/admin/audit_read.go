package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// PriceDetailActions names the audit actions whose detail holds a price, a rate or the active
// currency: the detail is withheld from a reader without price_list:read (ADR 0065). It is the one
// definition of what is withheld.
var PriceDetailActions = map[string]bool{
	"tenant_price.create":           true,
	"tenant_price.delete":           true,
	"tenant.active_currency_update": true,
}

// ListAuditEvents is a stub until sprint 6.2 part C2.
func (s *server) ListAuditEvents(context.Context, adminapi.ListAuditEventsInput) (*adminapi.AuditEventList, error) {
	return nil, errNotImplemented
}

// GetAuditHead is a stub until sprint 6.2 part C2.
func (s *server) GetAuditHead(context.Context, adminapi.GetAuditHeadInput) (*adminapi.AuditHead, error) {
	return nil, errNotImplemented
}

// ListPlatformAuditEvents is a stub until sprint 6.2 part C2.
func (s *server) ListPlatformAuditEvents(context.Context, adminapi.ListPlatformAuditEventsInput) (*adminapi.AuditEventList, error) {
	return nil, errNotImplemented
}

// GetPlatformAuditHead is a stub until sprint 6.2 part C2.
func (s *server) GetPlatformAuditHead(context.Context, adminapi.GetPlatformAuditHeadInput) (*adminapi.AuditHead, error) {
	return nil, errNotImplemented
}
