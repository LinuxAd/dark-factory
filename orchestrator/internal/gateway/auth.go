package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/LinuxAd/dark-factory/orchestrator/internal/store"
)

// tokenPrefix marks factory job tokens so they are easy to spot in a leak scan.
const tokenPrefix = "dft_"

// Caller identifies who a request is billed to; it comes from the token.
type Caller struct {
	RunID  string
	TaskID string
	Stage  string
}

// MintToken returns a new random job token and the hash to store for it.
func MintToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("read random bytes: %w", err)
	}
	token = tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is the lookup key stored in gateway_tokens.token_sha256.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// bearerToken returns the token from "Authorization: Bearer <token>".
func bearerToken(h http.Header) (string, bool) {
	v := h.Get("Authorization")
	scheme, tok, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}

var (
	errTokenInvalid   = errors.New("invalid, expired or revoked token")
	errHeaderMismatch = errors.New("X-Factory header does not match token")
)

// checkToken applies validity and the header rule: the token is
// authoritative, so any X-Factory-* header present must equal its value.
func checkToken(t store.Token, h http.Header, now time.Time) (Caller, error) {
	if !now.Before(t.ExpiresAt) || t.RevokedAt != nil {
		return Caller{}, errTokenInvalid
	}
	c := Caller{RunID: t.RunID, TaskID: t.TaskID, Stage: t.Stage}
	for name, want := range map[string]string{
		"X-Factory-Run":   c.RunID,
		"X-Factory-Task":  c.TaskID,
		"X-Factory-Stage": c.Stage,
	} {
		for _, got := range h.Values(name) {
			if got != want {
				return Caller{}, errHeaderMismatch
			}
		}
	}
	return c, nil
}
