package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// ListSubjectMappings: List the subject mappings of a tenant
func (s *server) ListSubjectMappings(_ context.Context, _ adminapi.ListSubjectMappingsInput) (*adminapi.SubjectMappingList, error) {
	return nil, errNotImplemented
}

// CreateSubjectMapping: Let a token subject act as a service account
func (s *server) CreateSubjectMapping(_ context.Context, _ adminapi.CreateSubjectMappingInput) (*adminapi.SubjectMapping, error) {
	return nil, errNotImplemented
}

// GetSubjectMapping: Read a subject mapping
func (s *server) GetSubjectMapping(_ context.Context, _ adminapi.GetSubjectMappingInput) (*adminapi.SubjectMapping, error) {
	return nil, errNotImplemented
}

// DeleteSubjectMapping: Delete a subject mapping
func (s *server) DeleteSubjectMapping(_ context.Context, _ adminapi.DeleteSubjectMappingInput) (*adminapi.SubjectMapping, error) {
	return nil, errNotImplemented
}
