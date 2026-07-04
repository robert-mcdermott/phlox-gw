package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

type fakeBedrockClient struct {
	input        *bedrockruntime.ConverseInput
	streamInput  *bedrockruntime.ConverseStreamInput
	streamEvents []types.ConverseStreamOutput
	streamErr    error
}

type fakeBedrockStream struct {
	events chan types.ConverseStreamOutput
	err    error
}

func newFakeBedrockStream(events []types.ConverseStreamOutput, err error) *fakeBedrockStream {
	ch := make(chan types.ConverseStreamOutput, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return &fakeBedrockStream{events: ch, err: err}
}

type fakeOIDCAuthenticator struct {
	state       string
	nonce       string
	redirectURL string
	claims      OIDCClaims
}

func jsonRequest(t *testing.T, handler http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return jsonRequestWithHeaders(t, handler, method, path, headers, body)
}

func jsonRequestWithHeaders(t *testing.T, handler http.Handler, method, path string, headers map[string]string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func sessionToken(t *testing.T, user store.User) string {
	t.Helper()
	token, err := auth.SignSession(auth.Claims{
		Subject:  user.ID,
		Username: user.Username,
		Role:     user.Role,
		IssuedAt: time.Now().UTC().Unix(),
		Expires:  time.Now().UTC().Add(time.Hour).Unix(),
	}, "test-secret")
	if err != nil {
		t.Fatalf("SignSession: %v", err)
	}
	return token
}

func decodeRecorder(t *testing.T, resp *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		t.Fatalf("Decode: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)
