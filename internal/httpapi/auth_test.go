package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func TestOIDCLoginCallbackProvisionsUserAndIssuesSession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	fake := &fakeOIDCAuthenticator{claims: OIDCClaims{
		Subject: "oidc-subject-1",
		Values: map[string]any{
			"preferred_username": "sso.user@example.com",
			"email":              "sso.user@example.com",
			"name":               "SSO User",
			"department":         "Finance",
			"groups":             []any{"phlox-admins"},
		},
	}}
	handler, err := New(Options{
		Config: config.Config{
			SessionSecret: "test-secret",
			OIDC: config.OIDCConfig{
				Enabled:         true,
				DisplayName:     "Entra ID",
				IssuerURL:       "https://login.example/tenant/v2.0",
				ClientID:        "client-id",
				ClientSecret:    "client-secret",
				Scopes:          []string{"openid", "profile", "email"},
				UsernameClaim:   "preferred_username",
				DepartmentClaim: "department",
				GroupsClaim:     "groups",
				AdminGroups:     []string{"phlox-admins"},
				AutoProvision:   true,
			},
		},
		Store: st,
		Frontend: fstest.MapFS{
			"frontend/dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		},
		OIDCAuthenticator: fake,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	configResp := httptest.NewRecorder()
	handler.ServeHTTP(configResp, httptest.NewRequest(http.MethodGet, "http://phlox.example/api/auth/oidc/config", nil))
	if configResp.Code != http.StatusOK {
		t.Fatalf("config status = %d body = %s", configResp.Code, configResp.Body.String())
	}

	loginResp := httptest.NewRecorder()
	handler.ServeHTTP(loginResp, httptest.NewRequest(http.MethodGet, "http://phlox.example/api/auth/oidc/login?return_to=/admin", nil))
	if loginResp.Code != http.StatusFound {
		t.Fatalf("login status = %d body = %s", loginResp.Code, loginResp.Body.String())
	}
	if fake.state == "" || fake.nonce == "" || fake.redirectURL != "http://phlox.example/api/auth/oidc/callback" {
		t.Fatalf("fake authenticator was not called correctly: %#v", fake)
	}
	cookies := loginResp.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != oidcStateCookieName {
		t.Fatalf("expected state cookie, got %#v", cookies)
	}

	callbackReq := httptest.NewRequest(http.MethodGet, "http://phlox.example/api/auth/oidc/callback?code=auth-code&state="+fake.state, nil)
	callbackReq.AddCookie(cookies[0])
	callbackResp := httptest.NewRecorder()
	handler.ServeHTTP(callbackResp, callbackReq)
	if callbackResp.Code != http.StatusOK {
		t.Fatalf("callback status = %d body = %s", callbackResp.Code, callbackResp.Body.String())
	}
	token := tokenFromOIDCHTML(t, callbackResp.Body.String())
	meResp := jsonRequest(t, handler, http.MethodGet, "/api/auth/me", token, nil)
	if meResp.Code != http.StatusOK {
		t.Fatalf("me status = %d body = %s", meResp.Code, meResp.Body.String())
	}
	var me struct {
		Username     string `json:"username"`
		DisplayName  string `json:"display_name"`
		Email        string `json:"email"`
		Department   string `json:"department"`
		Role         string `json:"role"`
		AuthProvider string `json:"auth_provider"`
	}
	decodeRecorder(t, meResp, &me)
	if me.Username != "sso.user@example.com" || me.DisplayName != "SSO User" || me.Email != "sso.user@example.com" || me.Department != "Finance" || me.Role != "admin" || me.AuthProvider != "oidc" {
		t.Fatalf("unexpected user profile: %#v", me)
	}
	if !strings.Contains(callbackResp.Body.String(), `window.location.replace("/admin")`) {
		t.Fatalf("callback did not preserve return target: %s", callbackResp.Body.String())
	}
}

func tokenFromOIDCHTML(t *testing.T, body string) string {
	t.Helper()
	marker := "localStorage.setItem('phlox_gw_token', "
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("missing token marker in %s", body)
	}
	rest := body[start+len(marker):]
	end := strings.Index(rest, ");")
	if end < 0 {
		t.Fatalf("missing token terminator in %s", body)
	}
	var token string
	if err := json.Unmarshal([]byte(rest[:end]), &token); err != nil {
		t.Fatalf("token JSON: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	return token
}
