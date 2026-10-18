package gateway

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LinuxAd/dark-factory/orchestrator/internal/store"
)

func TestMintTokenHash(t *testing.T) {
	tok, hash, err := MintToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "dft_") || len(tok) != 4+43 {
		t.Errorf("token %q has unexpected shape", tok)
	}
	want := sha256.Sum256([]byte(tok))
	if !bytes.Equal(hash, want[:]) || !bytes.Equal(HashToken(tok), want[:]) {
		t.Error("hash is not sha256 of the token")
	}
	tok2, _, _ := MintToken()
	if tok2 == tok {
		t.Error("two tokens are equal")
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]struct {
		v    string
		want string
		ok   bool
	}{
		"bearer":       {"Bearer abc", "abc", true},
		"lower":        {"bearer abc", "abc", true},
		"missing":      {"", "", false},
		"basic":        {"Basic abc", "", false},
		"empty token":  {"Bearer  ", "", false},
		"no separator": {"Bearerabc", "", false},
	}
	for name, tc := range cases {
		h := http.Header{}
		if tc.v != "" {
			h.Set("Authorization", tc.v)
		}
		got, ok := bearerToken(h)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %q %v", name, got, ok)
		}
	}
}

func TestCheckToken(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	revoked := now.Add(-time.Minute)
	valid := store.Token{RunID: "run-1", TaskID: "task-1", Stage: "build", ExpiresAt: now.Add(time.Hour)}
	noTask := store.Token{RunID: "run-1", Stage: "build", ExpiresAt: now.Add(time.Hour)}
	cases := []struct {
		name    string
		tok     store.Token
		headers map[string]string
		want    error
	}{
		{"valid, no headers", valid, nil, nil},
		{"matching headers", valid, map[string]string{"X-Factory-Run": "run-1", "X-Factory-Task": "task-1", "X-Factory-Stage": "build"}, nil},
		{"mismatching run", valid, map[string]string{"X-Factory-Run": "run-2"}, errHeaderMismatch},
		{"mismatching stage", valid, map[string]string{"X-Factory-Stage": "verify"}, errHeaderMismatch},
		{"task header on task-less token", noTask, map[string]string{"X-Factory-Task": "task-1"}, errHeaderMismatch},
		{"expired", store.Token{RunID: "r", Stage: "s", ExpiresAt: now}, nil, errTokenInvalid},
		{"revoked", store.Token{RunID: "r", Stage: "s", ExpiresAt: now.Add(time.Hour), RevokedAt: &revoked}, nil, errTokenInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			c, err := checkToken(tc.tok, h, now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if err == nil && (c.RunID != tc.tok.RunID || c.TaskID != tc.tok.TaskID || c.Stage != tc.tok.Stage) {
				t.Errorf("caller %+v does not come from token", c)
			}
		})
	}
}

func TestBackoffCappedAt30s(t *testing.T) {
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for n, w := range want {
		if got := backoff(time.Second, n); got != w*time.Second {
			t.Errorf("attempt %d: got %v, want %v", n, got, w*time.Second)
		}
	}
	if got := backoff(time.Second, 1000); got != 30*time.Second {
		t.Errorf("large attempt: got %v", got)
	}
}
