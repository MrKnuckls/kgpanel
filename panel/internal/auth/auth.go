package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Tokens are signed HMAC-SHA256 over {sub, exp}. No external JWT dep needed;
// compact and verifiable in the panel itself.

type Claims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
}

// HashPassword wraps bcrypt.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword verifies a bcrypt hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// Sign issues a token for user id, valid for 30 days.
func Sign(secret, userID string) (string, error) {
	claims, err := json.Marshal(Claims{Sub: userID, Exp: time.Now().Add(30 * 24 * time.Hour).Unix()})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Verify parses and checks a token, returning the subject (user id).
func Verify(secret, token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("malformed token")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0]))
	want, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("bad signature encoding")
	}
	if !hmac.Equal(want, mac.Sum(nil)) {
		return "", fmt.Errorf("bad signature")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("bad payload encoding")
	}
	var c Claims
	if err := json.Unmarshal(b, &c); err != nil {
		return "", fmt.Errorf("bad payload")
	}
	if time.Now().Unix() > c.Exp {
		return "", fmt.Errorf("expired")
	}
	return c.Sub, nil
}

// NewSecret returns a random hex secret for token signing.
func NewSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Bearer extracts the Authorization header value ("Bearer <token>").
func Bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return h[7:]
	}
	return ""
}
