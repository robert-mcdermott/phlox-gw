package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func requestIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[0])
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func requestEventFromHTTP(r *http.Request, streaming bool) requestEventMeta {
	return requestEventMeta{
		Method:    r.Method,
		Endpoint:  r.URL.Path,
		Streaming: streaming,
		ClientIP:  requestIP(r),
		UserAgent: limitString(r.UserAgent(), 500),
	}
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (s *Server) requireSession(next func(http.ResponseWriter, *http.Request, store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			respondError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := auth.VerifySession(token, s.cfg.SessionSecret, time.Now().UTC())
		if err != nil {
			respondError(w, http.StatusUnauthorized, "invalid session")
			return
		}
		user, err := s.store.GetUserByID(r.Context(), claims.Subject)
		if err != nil || !user.IsActive {
			respondError(w, http.StatusUnauthorized, "invalid session")
			return
		}
		next(w, r, user)
	}
}

func (s *Server) requireAdmin(next func(http.ResponseWriter, *http.Request, store.User)) http.HandlerFunc {
	return s.requireSession(func(w http.ResponseWriter, r *http.Request, user store.User) {
		if user.Role != "admin" {
			respondError(w, http.StatusForbidden, "admin required")
			return
		}
		next(w, r, user)
	})
}

func (s *Server) requireAPIKey(next func(http.ResponseWriter, *http.Request, store.User, store.APIKey)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := gatewayAPIKeyToken(r)
		if token == "" {
			gatewayAuthError(w, r, http.StatusUnauthorized, "missing API key")
			return
		}
		user, key, err := s.store.ResolveAPIKey(r.Context(), auth.HashAPIKey(token), time.Now().UTC())
		if err != nil {
			gatewayAuthError(w, r, http.StatusUnauthorized, "invalid API key")
			return
		}
		next(w, r, user, key)
	}
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	f, err := s.frontend.Open(path)
	if err != nil {
		path = "index.html"
		f, err = s.frontend.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	defer f.Close()
	http.ServeContent(w, r, path, time.Time{}, readSeeker{f})
}

func readObjectBody(w http.ResponseWriter, r *http.Request, openAIShape bool) ([]byte, map[string]any, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if err != nil {
		respondError(w, http.StatusBadRequest, "could not read body")
		return nil, nil, false
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		if openAIShape {
			openAIError(w, http.StatusBadRequest, "invalid JSON", "invalid_request_error")
		} else {
			anthropicError(w, http.StatusBadRequest, "invalid JSON")
		}
		return nil, nil, false
	}
	return body, raw, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func parseAPIKeyExpiresAt(w http.ResponseWriter, raw string, requireFuture bool) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		respondError(w, http.StatusBadRequest, "expires_at must be RFC3339")
		return nil, false
	}
	t = t.UTC()
	if requireFuture && !t.After(time.Now().UTC()) {
		respondError(w, http.StatusBadRequest, "expires_at must be in the future")
		return nil, false
	}
	return &t, true
}

func respondJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]any{"error": map[string]any{"message": message}})
}

func openAIError(w http.ResponseWriter, status int, message, typ string) {
	respondJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": typ}})
}

func anthropicError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": message}})
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func gatewayAPIKeyToken(r *http.Request) string {
	if token := bearerToken(r); token != "" {
		return token
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

func gatewayAuthError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.HasPrefix(r.URL.Path, "/anthropic/") {
		anthropicError(w, status, message)
		return
	}
	openAIError(w, status, message, "authentication_error")
}

func publicUser(u store.User) map[string]any {
	return map[string]any{
		"id":            u.ID,
		"username":      u.Username,
		"email":         u.Email,
		"display_name":  u.DisplayName,
		"department":    u.Department,
		"role":          u.Role,
		"auth_provider": u.AuthProvider,
		"is_active":     u.IsActive,
	}
}

func shouldProxyHeader(name string) bool {
	switch strings.ToLower(name) {
	case "content-type", "cache-control", "x-request-id":
		return true
	default:
		return false
	}
}

func requestID() string {
	id, err := auth.RandomID("req")
	if err != nil {
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return id
}

func limitString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func secretMarker(env string) string {
	if env == "" {
		return ""
	}
	return " (secret set)"
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

type readSeeker struct {
	fs.File
}

func (r readSeeker) Seek(offset int64, whence int) (int64, error) {
	if seeker, ok := r.File.(io.Seeker); ok {
		return seeker.Seek(offset, whence)
	}
	return 0, errors.New("file is not seekable")
}
