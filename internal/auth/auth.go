package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Claims struct {
	Subject  string `json:"sub"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Expires  int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
}

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func SignSession(claims Claims, secret string) (string, error) {
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	headerPart := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsPart := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerPart + "." + claimsPart
	sig := sign(signingInput, secret)
	return signingInput + "." + sig, nil
}

func VerifySession(token, secret string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("invalid token")
	}
	signingInput := parts[0] + "." + parts[1]
	expected := sign(signingInput, secret)
	if !hmac.Equal([]byte(parts[2]), []byte(expected)) {
		return Claims{}, errors.New("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, err
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, err
	}
	if claims.Expires <= now.Unix() {
		return Claims{}, errors.New("token expired")
	}
	return claims, nil
}

func NewAPIKey() (plain string, prefix string, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", err
	}
	plain = "pgw-sk-" + base64.RawURLEncoding.EncodeToString(buf)
	prefix = plain
	if len(prefix) > 18 {
		prefix = prefix[:18]
	}
	hash = HashAPIKey(plain)
	return plain, prefix, hash, nil
}

func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func RandomID(prefix string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	if prefix == "" {
		return base64.RawURLEncoding.EncodeToString(buf), nil
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func sign(input, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(input))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
