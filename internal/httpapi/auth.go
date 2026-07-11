package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	oidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
	"golang.org/x/oauth2"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	user, err := s.store.GetUserByUsername(r.Context(), req.Username)
	if err != nil || !user.IsActive || !auth.CheckPassword(user.PasswordHash, req.Password) {
		respondError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	token, err := s.issueSessionToken(user)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not sign session")
		return
	}
	now := time.Now().UTC()
	_ = s.store.TouchLogin(r.Context(), user.ID, now)
	s.audit(r, user, "auth.login", "user", user.ID, user.Username, map[string]any{
		"auth_provider": user.AuthProvider,
		"role":          user.Role,
	})
	respondJSON(w, http.StatusOK, map[string]any{"token": token, "user": publicUser(user)})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, user store.User) {
	if !user.MustChangePassword {
		respondError(w, http.StatusConflict, "password change is not required")
		return
	}
	if user.AuthProvider != "local" {
		respondError(w, http.StatusBadRequest, "password changes are only available for local accounts")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if size := len([]byte(req.Password)); size < 12 || size > 72 {
		respondError(w, http.StatusBadRequest, "password must be between 12 and 72 bytes")
		return
	}
	if auth.CheckPassword(user.PasswordHash, req.Password) {
		respondError(w, http.StatusBadRequest, "new password must differ from the temporary password")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := s.store.CompleteRequiredPasswordChange(r.Context(), user.ID, hash, user.SessionVersion); err != nil {
		if errors.Is(err, store.ErrConflict) {
			respondError(w, http.StatusConflict, "password was already changed; sign in again")
			return
		}
		respondError(w, http.StatusInternalServerError, "could not update password")
		return
	}
	updated, err := s.store.GetUserByID(r.Context(), user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not reload user")
		return
	}
	token, err := s.issueSessionToken(updated)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not sign session")
		return
	}
	s.audit(r, updated, "auth.password_changed", "user", updated.ID, updated.Username, map[string]any{"forced": true})
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "token": token, "user": publicUser(updated)})
}

func (s *Server) oidcConfig(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{
		"enabled":      s.cfg.OIDC.Enabled,
		"display_name": s.cfg.OIDC.DisplayName,
	})
}

func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDC.Enabled || s.oidcAuthenticator == nil {
		respondError(w, http.StatusNotFound, "OIDC login is not enabled")
		return
	}
	state, err := randomOIDCToken()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not create OIDC state")
		return
	}
	nonce, err := randomOIDCToken()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not create OIDC nonce")
		return
	}
	loginState := oidcLoginState{
		State:    state,
		Nonce:    nonce,
		ReturnTo: cleanReturnTo(r.URL.Query().Get("return_to")),
		Expires:  time.Now().UTC().Add(10 * time.Minute).Unix(),
	}
	cookieValue, err := s.signOIDCState(loginState)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not sign OIDC state")
		return
	}
	http.SetCookie(w, s.oidcStateCookie(r, cookieValue, 10*60))
	authURL, err := s.oidcAuthenticator.AuthCodeURL(r.Context(), state, nonce, s.oidcRedirectURL(r))
	if err != nil {
		respondError(w, http.StatusBadGateway, "OIDC provider configuration failed: "+err.Error())
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDC.Enabled || s.oidcAuthenticator == nil {
		respondOIDCError(w, http.StatusNotFound, "OIDC login is not enabled")
		return
	}
	if providerError := strings.TrimSpace(r.URL.Query().Get("error")); providerError != "" {
		description := strings.TrimSpace(r.URL.Query().Get("error_description"))
		if description != "" {
			providerError += ": " + description
		}
		respondOIDCError(w, http.StatusUnauthorized, providerError)
		return
	}
	loginState, ok := s.readOIDCState(r)
	if !ok {
		respondOIDCError(w, http.StatusUnauthorized, "OIDC state is missing or invalid")
		return
	}
	if loginState.Expires <= time.Now().UTC().Unix() {
		respondOIDCError(w, http.StatusUnauthorized, "OIDC state expired")
		return
	}
	if subtleState := strings.TrimSpace(r.URL.Query().Get("state")); subtleState == "" || !hmac.Equal([]byte(subtleState), []byte(loginState.State)) {
		respondOIDCError(w, http.StatusUnauthorized, "OIDC state mismatch")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		respondOIDCError(w, http.StatusBadRequest, "OIDC authorization code is missing")
		return
	}
	claims, err := s.oidcAuthenticator.Exchange(r.Context(), code, loginState.Nonce, s.oidcRedirectURL(r))
	if err != nil {
		respondOIDCError(w, http.StatusUnauthorized, "OIDC token exchange failed: "+err.Error())
		return
	}
	user, err := s.userFromOIDCClaims(r.Context(), claims)
	if err != nil {
		if errors.Is(err, errOIDCProvisioningDisabled) {
			respondOIDCError(w, http.StatusForbidden, "SSO user is not provisioned in Phlox-GW")
			return
		}
		if errors.Is(err, errOIDCUserDisabled) {
			respondOIDCError(w, http.StatusForbidden, "SSO user is disabled in Phlox-GW")
			return
		}
		respondOIDCError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, err := s.issueSessionToken(user)
	if err != nil {
		respondOIDCError(w, http.StatusInternalServerError, "could not sign session")
		return
	}
	http.SetCookie(w, s.oidcStateCookie(r, "", -1))
	_ = s.store.TouchLogin(r.Context(), user.ID, time.Now().UTC())
	s.audit(r, user, "auth.login", "user", user.ID, user.Username, map[string]any{
		"auth_provider": "oidc",
		"issuer":        s.cfg.OIDC.IssuerURL,
		"role":          user.Role,
		"department":    user.Department,
	})
	respondOIDCSuccess(w, token, loginState.ReturnTo)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, user store.User) {
	respondJSON(w, http.StatusOK, publicUser(user))
}

var errOIDCProvisioningDisabled = errors.New("oidc provisioning disabled")

var errOIDCUserDisabled = errors.New("oidc user disabled")

type defaultOIDCAuthenticator struct {
	cfg        config.OIDCConfig
	httpClient *http.Client
	mu         sync.Mutex
	provider   *oidc.Provider
}

func newDefaultOIDCAuthenticator(cfg config.OIDCConfig, httpClient *http.Client) *defaultOIDCAuthenticator {
	return &defaultOIDCAuthenticator{cfg: cfg, httpClient: httpClient}
}

func (a *defaultOIDCAuthenticator) AuthCodeURL(ctx context.Context, state, nonce, redirectURL string) (string, error) {
	provider, err := a.providerFor(ctx)
	if err != nil {
		return "", err
	}
	cfg := a.oauth2Config(provider, redirectURL)
	return cfg.AuthCodeURL(state, oidc.Nonce(nonce)), nil
}

func (a *defaultOIDCAuthenticator) Exchange(ctx context.Context, code, nonce, redirectURL string) (OIDCClaims, error) {
	provider, err := a.providerFor(ctx)
	if err != nil {
		return OIDCClaims{}, err
	}
	ctx = a.clientContext(ctx)
	cfg := a.oauth2Config(provider, redirectURL)
	oauthToken, err := cfg.Exchange(ctx, code)
	if err != nil {
		return OIDCClaims{}, err
	}
	rawIDToken, ok := oauthToken.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return OIDCClaims{}, errors.New("OIDC provider did not return an id_token")
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: a.cfg.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return OIDCClaims{}, err
	}
	if idToken.Nonce != nonce {
		return OIDCClaims{}, errors.New("OIDC nonce mismatch")
	}
	values := map[string]any{}
	if err := idToken.Claims(&values); err != nil {
		return OIDCClaims{}, err
	}
	return OIDCClaims{Subject: idToken.Subject, Values: values}, nil
}

func (a *defaultOIDCAuthenticator) providerFor(ctx context.Context) (*oidc.Provider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.provider != nil {
		return a.provider, nil
	}
	provider, err := oidc.NewProvider(a.clientContext(ctx), a.cfg.IssuerURL)
	if err != nil {
		return nil, err
	}
	a.provider = provider
	return provider, nil
}

func (a *defaultOIDCAuthenticator) clientContext(ctx context.Context) context.Context {
	if a.httpClient == nil {
		return ctx
	}
	return oidc.ClientContext(ctx, a.httpClient)
}

func (a *defaultOIDCAuthenticator) oauth2Config(provider *oidc.Provider, redirectURL string) oauth2.Config {
	return oauth2.Config{
		ClientID:     a.cfg.ClientID,
		ClientSecret: a.cfg.ClientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       a.cfg.Scopes,
	}
}

type oidcLoginState struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	ReturnTo string `json:"return_to"`
	Expires  int64  `json:"expires"`
}

const oidcStateCookieName = "phlox_gw_oidc_state"

func (s *Server) userFromOIDCClaims(ctx context.Context, claims OIDCClaims) (store.User, error) {
	username := firstClaimString(claims.Values, s.cfg.OIDC.UsernameClaim, "preferred_username", "upn", "email")
	if username == "" {
		username = claims.Subject
	}
	if username == "" {
		return store.User{}, errors.New("OIDC subject or username claim is required")
	}
	email := firstClaimString(claims.Values, "email", "preferred_username", "upn")
	displayName := firstClaimString(claims.Values, "name", "given_name")
	department := firstClaimString(claims.Values, s.cfg.OIDC.DepartmentClaim)
	if displayName == "" {
		displayName = username
	}
	existing, err := s.store.GetUserByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		if !s.cfg.OIDC.AutoProvision {
			return store.User{}, errOIDCProvisioningDisabled
		}
		id, idErr := auth.RandomID("user")
		if idErr != nil {
			return store.User{}, idErr
		}
		user := store.User{
			ID:           id,
			Username:     username,
			Email:        email,
			DisplayName:  displayName,
			Department:   department,
			Role:         s.oidcRoleForClaims("", claims.Values),
			PasswordHash: "",
			AuthProvider: "oidc",
			IsActive:     true,
		}
		if err := s.store.CreateUser(ctx, user); err != nil {
			return store.User{}, err
		}
		return s.store.GetUserByID(ctx, user.ID)
	}
	if err != nil {
		return store.User{}, err
	}
	if !existing.IsActive {
		return store.User{}, errOIDCUserDisabled
	}
	existing.Email = email
	existing.DisplayName = displayName
	existing.Department = department
	existing.Role = s.oidcRoleForClaims(existing.Role, claims.Values)
	if existing.AuthProvider == "" || existing.AuthProvider == "oidc" {
		existing.AuthProvider = "oidc"
	}
	if err := s.store.UpdateFederatedUser(ctx, existing); err != nil {
		return store.User{}, err
	}
	return s.store.GetUserByID(ctx, existing.ID)
}

func (s *Server) oidcRoleForClaims(existingRole string, values map[string]any) string {
	role := existingRole
	if role == "" {
		role = "user"
	}
	if groupsIntersect(claimStrings(values[s.cfg.OIDC.GroupsClaim]), s.cfg.OIDC.AdminGroups) {
		return "admin"
	}
	return role
}

func firstClaimString(values map[string]any, names ...string) string {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for _, value := range claimStrings(values[name]) {
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func claimStrings(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{strings.TrimSpace(v)}
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text, ok := item.(string); ok {
				if text = strings.TrimSpace(text); text != "" {
					out = append(out, text)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func groupsIntersect(claimGroups, adminGroups []string) bool {
	if len(claimGroups) == 0 || len(adminGroups) == 0 {
		return false
	}
	set := map[string]struct{}{}
	for _, group := range claimGroups {
		set[strings.ToLower(group)] = struct{}{}
	}
	for _, group := range adminGroups {
		if _, ok := set[strings.ToLower(strings.TrimSpace(group))]; ok {
			return true
		}
	}
	return false
}

func (s *Server) issueSessionToken(user store.User) (string, error) {
	now := time.Now().UTC()
	claims := auth.Claims{
		Subject:        user.ID,
		Username:       user.Username,
		Role:           user.Role,
		SessionVersion: user.SessionVersion,
		IssuedAt:       now.Unix(),
		Expires:        now.Add(12 * time.Hour).Unix(),
	}
	return auth.SignSession(claims, s.cfg.SessionSecret)
}

func (s *Server) oidcRedirectURL(r *http.Request) string {
	if s.cfg.OIDC.RedirectURL != "" {
		return s.cfg.OIDC.RedirectURL
	}
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/auth/oidc/callback"
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func cleanReturnTo(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return "/"
	}
	return value
}

func randomOIDCToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (s *Server) signOIDCState(state oidcLoginState) (string, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	payloadText := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte(payloadText))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payloadText + "." + signature, nil
}

func (s *Server) readOIDCState(r *http.Request) (oidcLoginState, bool) {
	cookie, err := r.Cookie(oidcStateCookieName)
	if err != nil || cookie.Value == "" {
		return oidcLoginState{}, false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return oidcLoginState{}, false
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(expected)) {
		return oidcLoginState{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return oidcLoginState{}, false
	}
	var state oidcLoginState
	if err := json.Unmarshal(payload, &state); err != nil {
		return oidcLoginState{}, false
	}
	return state, true
}

func (s *Server) oidcStateCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    value,
		Path:     "/api/auth/oidc",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	}
}

func respondOIDCSuccess(w http.ResponseWriter, token, returnTo string) {
	tokenJSON, _ := json.Marshal(token)
	returnJSON, _ := json.Marshal(cleanReturnTo(returnTo))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Signing in...</title></head><body><script>localStorage.setItem('phlox_gw_token', %s); window.location.replace(%s);</script></body></html>`, tokenJSON, returnJSON)
}

func respondOIDCError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	payload, _ := json.Marshal(message)
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Sign in failed</title></head><body><main><h1>Sign in failed</h1><p id="message"></p><script>document.getElementById('message').textContent = %s;</script></main></body></html>`, payload)
}
