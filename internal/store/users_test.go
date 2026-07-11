package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func TestEnsureBootstrapDataCreatesForcedAdminOnce(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	first, err := s.EnsureBootstrapData("first-hash")
	if err != nil {
		t.Fatalf("EnsureBootstrapData first: %v", err)
	}
	if !first.AdminCreated {
		t.Fatal("first bootstrap did not report an administrator creation")
	}
	admin, err := s.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if admin.PasswordHash != "first-hash" || !admin.MustChangePassword || admin.SessionVersion != 0 {
		t.Fatalf("unexpected bootstrap administrator: %#v", admin)
	}

	second, err := s.EnsureBootstrapData("second-hash")
	if err != nil {
		t.Fatalf("EnsureBootstrapData second: %v", err)
	}
	if second.AdminCreated {
		t.Fatal("second bootstrap reported an administrator creation")
	}
	admin, err = s.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername second: %v", err)
	}
	if admin.PasswordHash != "first-hash" {
		t.Fatal("second bootstrap replaced the existing password")
	}
}

func TestSetUserPasswordChangesRequirementAndInvalidatesSessions(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	user := User{
		ID:                 "user_password",
		Username:           "password-user",
		Role:               "user",
		PasswordHash:       "temporary-hash",
		AuthProvider:       "local",
		IsActive:           true,
		MustChangePassword: true,
	}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.SetUserPassword(ctx, user.ID, "permanent-hash", false); err != nil {
		t.Fatalf("SetUserPassword: %v", err)
	}
	updated, err := s.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if updated.PasswordHash != "permanent-hash" || updated.MustChangePassword || updated.SessionVersion != 1 {
		t.Fatalf("unexpected updated user: %#v", updated)
	}
}

func TestCompleteRequiredPasswordChangeRejectsStaleAttempt(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	user := User{
		ID:                 "user_one_time_change",
		Username:           "one-time-change",
		Role:               "admin",
		PasswordHash:       "temporary-hash",
		AuthProvider:       "local",
		IsActive:           true,
		MustChangePassword: true,
	}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.CompleteRequiredPasswordChange(ctx, user.ID, "first-permanent-hash", 0); err != nil {
		t.Fatalf("CompleteRequiredPasswordChange: %v", err)
	}
	if err := s.CompleteRequiredPasswordChange(ctx, user.ID, "attacker-hash", 0); err != ErrConflict {
		t.Fatalf("stale password change error = %v, want ErrConflict", err)
	}
	updated, err := s.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if updated.PasswordHash != "first-permanent-hash" || updated.SessionVersion != 1 || updated.MustChangePassword {
		t.Fatalf("stale attempt changed user: %#v", updated)
	}
}

func TestEnsureBootstrapDataConcurrentCallsCreateOneAdmin(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	const callers = 8
	results := make(chan SeedResult, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.EnsureBootstrapData("concurrent-hash")
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("EnsureBootstrapData: %v", err)
		}
	}
	created := 0
	for result := range results {
		if result.AdminCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("administrator creation reports = %d, want 1", created)
	}
	users, err := s.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].Username != "admin" {
		t.Fatalf("users = %#v, want one administrator", users)
	}
}
