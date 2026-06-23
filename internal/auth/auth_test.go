package auth

import (
	"testing"
	"time"
)

func TestSessionTokenRoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	claims := Claims{
		Subject:  "user_123",
		Username: "alice",
		Role:     "admin",
		IssuedAt: now.Unix(),
		Expires:  now.Add(time.Hour).Unix(),
	}
	token, err := SignSession(claims, "test-secret")
	if err != nil {
		t.Fatalf("SignSession: %v", err)
	}
	got, err := VerifySession(token, "test-secret", now)
	if err != nil {
		t.Fatalf("VerifySession: %v", err)
	}
	if got.Subject != claims.Subject || got.Username != claims.Username || got.Role != claims.Role {
		t.Fatalf("claims mismatch: got %#v want %#v", got, claims)
	}
	if _, err := VerifySession(token, "wrong-secret", now); err == nil {
		t.Fatal("VerifySession accepted a token signed with a different secret")
	}
}

func TestAPIKeyHashing(t *testing.T) {
	plain, prefix, hash, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if plain == "" || prefix == "" || hash == "" {
		t.Fatalf("expected key material, got plain=%q prefix=%q hash=%q", plain, prefix, hash)
	}
	if HashAPIKey(plain) != hash {
		t.Fatal("stored hash does not match generated API key")
	}
	if HashAPIKey(plain+"x") == hash {
		t.Fatal("different API key produced the same hash")
	}
}
