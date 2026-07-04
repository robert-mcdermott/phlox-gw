package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestSelfServiceAPIKeyExpirationAndRotation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	user := store.User{
		ID:           "user_keys",
		Username:     "key-user",
		Role:         "user",
		PasswordHash: "unused",
		AuthProvider: "local",
		IsActive:     true,
	}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session := sessionToken(t, user)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	createResp := jsonRequest(t, handler, http.MethodPost, "/api/api-keys", session, map[string]any{
		"name":       "Original key",
		"expires_at": expires.Format(time.RFC3339),
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", createResp.Code, createResp.Body.String())
	}
	var createBody struct {
		Key    string       `json:"key"`
		Record store.APIKey `json:"record"`
	}
	decodeRecorder(t, createResp, &createBody)
	if createBody.Key == "" || createBody.Record.ID == "" || createBody.Record.ExpiresAt == nil {
		t.Fatalf("unexpected create body: %#v", createBody)
	}

	newExpires := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	patchResp := jsonRequest(t, handler, http.MethodPatch, "/api/api-keys/"+createBody.Record.ID, session, map[string]any{
		"name":       "Rotatable key",
		"expires_at": newExpires.Format(time.RFC3339),
	})
	if patchResp.Code != http.StatusOK {
		t.Fatalf("patch status = %d body = %s", patchResp.Code, patchResp.Body.String())
	}
	pastResp := jsonRequest(t, handler, http.MethodPatch, "/api/api-keys/"+createBody.Record.ID, session, map[string]any{
		"name":       "Bad expiration",
		"expires_at": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	})
	if pastResp.Code != http.StatusBadRequest {
		t.Fatalf("past expiration status = %d body = %s", pastResp.Code, pastResp.Body.String())
	}

	rotateResp := jsonRequest(t, handler, http.MethodPost, "/api/api-keys/"+createBody.Record.ID+"/rotate", session, map[string]any{})
	if rotateResp.Code != http.StatusOK {
		t.Fatalf("rotate status = %d body = %s", rotateResp.Code, rotateResp.Body.String())
	}
	var rotateBody struct {
		Key    string `json:"key"`
		Prefix string `json:"prefix"`
	}
	decodeRecorder(t, rotateResp, &rotateBody)
	if rotateBody.Key == "" || rotateBody.Key == createBody.Key || rotateBody.Prefix == createBody.Record.Prefix {
		t.Fatalf("unexpected rotate body: %#v", rotateBody)
	}
	oldResp := jsonRequest(t, handler, http.MethodGet, "/v1/models", createBody.Key, nil)
	if oldResp.Code != http.StatusUnauthorized {
		t.Fatalf("old key status = %d body = %s", oldResp.Code, oldResp.Body.String())
	}
	newResp := jsonRequest(t, handler, http.MethodGet, "/v1/models", rotateBody.Key, nil)
	if newResp.Code != http.StatusOK {
		t.Fatalf("new key status = %d body = %s", newResp.Code, newResp.Body.String())
	}

	listResp := jsonRequest(t, handler, http.MethodGet, "/api/api-keys", session, nil)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", listResp.Code, listResp.Body.String())
	}
	var keys []store.APIKey
	decodeRecorder(t, listResp, &keys)
	if len(keys) != 1 || keys[0].Name != "Rotatable key" || keys[0].Prefix != rotateBody.Prefix || keys[0].LastUsedAt == nil {
		t.Fatalf("unexpected key list: %#v", keys)
	}
}

func TestAdminCanRotateActiveAPIKey(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	admin := store.User{ID: "user_admin", Username: "admin", Role: "admin", PasswordHash: "unused", AuthProvider: "local", IsActive: true}
	user := store.User{ID: "user_owner", Username: "owner", Role: "user", PasswordHash: "unused", AuthProvider: "local", IsActive: true}
	if err := st.CreateUser(ctx, admin); err != nil {
		t.Fatalf("CreateUser admin: %v", err)
	}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser owner: %v", err)
	}
	oldPlain, oldPrefix, oldHash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := st.CreateAPIKey(ctx, store.APIKey{ID: "key_rotate_admin", UserID: user.ID, Name: "Owned key", Prefix: oldPrefix, KeyHash: oldHash}); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	handler, err := New(Options{
		Config: config.Config{SessionSecret: "test-secret"},
		Store:  st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rotateResp := jsonRequest(t, handler, http.MethodPost, "/api/admin/api-keys/key_rotate_admin/rotate", sessionToken(t, admin), map[string]any{})
	if rotateResp.Code != http.StatusOK {
		t.Fatalf("admin rotate status = %d body = %s", rotateResp.Code, rotateResp.Body.String())
	}
	var rotateBody struct {
		Key    string `json:"key"`
		Prefix string `json:"prefix"`
	}
	decodeRecorder(t, rotateResp, &rotateBody)
	if rotateBody.Key == "" || rotateBody.Key == oldPlain || rotateBody.Prefix == oldPrefix {
		t.Fatalf("unexpected rotate body: %#v", rotateBody)
	}
	oldResp := jsonRequest(t, handler, http.MethodGet, "/v1/models", oldPlain, nil)
	if oldResp.Code != http.StatusUnauthorized {
		t.Fatalf("old key status = %d body = %s", oldResp.Code, oldResp.Body.String())
	}
	newResp := jsonRequest(t, handler, http.MethodGet, "/v1/models", rotateBody.Key, nil)
	if newResp.Code != http.StatusOK {
		t.Fatalf("new key status = %d body = %s", newResp.Code, newResp.Body.String())
	}
}
