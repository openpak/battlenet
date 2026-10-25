package main

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accountv1 "github.com/openpak/account-api/openpak/account/v1"
)

type fakeAccounts struct {
	accountv1.IdentityClient
	status accountv1.AccountStatus
	err    error
}

func (f fakeAccounts) GetAccount(context.Context, *accountv1.GetAccountRequest, ...grpc.CallOption) (*accountv1.GetAccountResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &accountv1.GetAccountResponse{Status: f.status}, nil
}

// A ban set in the admin locks the account out of Battle.net sign-in.
func TestAccountAllowed(t *testing.T) {
	ctx := context.Background()
	if err := accountAllowed(ctx, fakeAccounts{status: accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE}, "a"); err != nil {
		t.Fatalf("active: %v", err)
	}
	for _, st := range []accountv1.AccountStatus{
		accountv1.AccountStatus_ACCOUNT_STATUS_BANNED,
		accountv1.AccountStatus_ACCOUNT_STATUS_DELETION_PENDING,
		accountv1.AccountStatus_ACCOUNT_STATUS_DELETED,
	} {
		if err := accountAllowed(ctx, fakeAccounts{status: st}, "a"); !errors.Is(err, errBanned) {
			t.Errorf("%s: want errBanned, got %v", st, err)
		}
	}
	if err := accountAllowed(ctx, fakeAccounts{err: status.Error(codes.NotFound, "no")}, "a"); !errors.Is(err, errBanned) {
		t.Errorf("unknown account: want errBanned, got %v", err)
	}
	err := accountAllowed(ctx, fakeAccounts{err: status.Error(codes.Unavailable, "down")}, "a")
	if err == nil || errors.Is(err, errBanned) || status.Code(err) != codes.Unavailable {
		t.Errorf("core down: want a refusal the caller retries (Unavailable), got %v", err)
	}
	if !errors.Is(errBanned, errUnauthenticated) {
		t.Error("a ban must take the existing refusal path")
	}
}
