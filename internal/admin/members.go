package admin

// Handlers of the operations on members of a tenant (sprint 4.2). Part A: each one answers with an error,
// which the router turns into a 500; steps C3 to C8 write them.

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// ListMembers is not implemented yet.
func (s *server) ListMembers(_ context.Context, _ adminapi.ListMembersInput) (*adminapi.MemberList, error) {
	return nil, errors.New("not implemented")
}

// CreateMember is not implemented yet.
func (s *server) CreateMember(_ context.Context, _ adminapi.CreateMemberInput) (*adminapi.MemberCreated, error) {
	return nil, errors.New("not implemented")
}

// GetMember is not implemented yet.
func (s *server) GetMember(_ context.Context, _ adminapi.GetMemberInput) (*adminapi.Member, error) {
	return nil, errors.New("not implemented")
}

// UpdateMember is not implemented yet.
func (s *server) UpdateMember(_ context.Context, _ adminapi.UpdateMemberInput) (*adminapi.Member, error) {
	return nil, errors.New("not implemented")
}

// DeleteMember is not implemented yet.
func (s *server) DeleteMember(_ context.Context, _ adminapi.DeleteMemberInput) (*adminapi.Member, error) {
	return nil, errors.New("not implemented")
}

// DisableMember is not implemented yet.
func (s *server) DisableMember(_ context.Context, _ adminapi.DisableMemberInput) (*adminapi.Member, error) {
	return nil, errors.New("not implemented")
}

// EnableMember is not implemented yet.
func (s *server) EnableMember(_ context.Context, _ adminapi.EnableMemberInput) (*adminapi.Member, error) {
	return nil, errors.New("not implemented")
}

// ResetMemberPassword is not implemented yet.
func (s *server) ResetMemberPassword(_ context.Context, _ adminapi.ResetMemberPasswordInput) (*adminapi.PasswordReset, error) {
	return nil, errors.New("not implemented")
}

// RevokeMemberSessions is not implemented yet.
func (s *server) RevokeMemberSessions(_ context.Context, _ adminapi.RevokeMemberSessionsInput) (*adminapi.SessionsRevoked, error) {
	return nil, errors.New("not implemented")
}

// UnlockMember is not implemented yet.
func (s *server) UnlockMember(_ context.Context, _ adminapi.UnlockMemberInput) (*adminapi.Member, error) {
	return nil, errors.New("not implemented")
}
