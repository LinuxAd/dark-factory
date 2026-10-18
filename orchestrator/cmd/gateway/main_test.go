package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LinuxAd/dark-factory/orchestrator/internal/gateway"
	"github.com/LinuxAd/dark-factory/orchestrator/internal/store"
	"github.com/LinuxAd/dark-factory/orchestrator/internal/testpg"
)

func TestMintTokenPrintsOnlyTheToken(t *testing.T) {
	ctx := context.Background()
	dsn := testpg.New(t)
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == "DATABASE_URL" {
			return dsn
		}
		return ""
	}
	var out, errOut bytes.Buffer
	code := run(ctx, []string{"mint-token", "--run", "phase0-smoke-1", "--task", "t1", "--stage", "smoke", "--ttl", "2h"}, env, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errOut.String())
	}
	tok := strings.TrimSuffix(out.String(), "\n")
	if !strings.HasPrefix(tok, "dft_") || strings.ContainsAny(tok, " \n") || errOut.Len() != 0 {
		t.Fatalf("stdout %q stderr %q: want only the token", out.String(), errOut.String())
	}
	got, err := st.LookupToken(ctx, gateway.HashToken(tok))
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != "phase0-smoke-1" || got.TaskID != "t1" || got.Stage != "smoke" {
		t.Errorf("stored %+v", got)
	}
	if d := time.Until(got.ExpiresAt); d < 119*time.Minute || d > 121*time.Minute {
		t.Errorf("expires in %v, want ~2h", d)
	}
}

func TestMintTokenRequiresRunAndStage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"mint-token", "--run", "r"}, func(string) string { return "" }, &out, &errOut); code == 0 {
		t.Fatal("want non-zero exit without --stage")
	}
	if out.Len() != 0 {
		t.Errorf("stdout %q, want empty", out.String())
	}
}

func TestServeRequiresSecretID(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"serve"}, func(string) string { return "" }, &out, &errOut); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(out.String(), "ANTHROPIC_KEY_SECRET_ID") {
		t.Errorf("log %q does not name the missing variable", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"nope"}, func(string) string { return "" }, &out, &errOut); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestHTTPServerLimits(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	if srv.IdleTimeout != 90*time.Second || srv.MaxHeaderBytes != 64<<10 || srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("IdleTimeout %v MaxHeaderBytes %d ReadHeaderTimeout %v", srv.IdleTimeout, srv.MaxHeaderBytes, srv.ReadHeaderTimeout)
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	req, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
	req.Header.Set("X-Big", strings.Repeat("a", 80<<10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("80 KiB header: status %d, want 431", resp.StatusCode)
	}
}

func TestGatewayConfigFromEnv(t *testing.T) {
	envOf := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cfg, err := gatewayConfig(envOf(map[string]string{"GATEWAY_MAX_INFLIGHT_PER_TOKEN": "3"}))
	if err != nil || cfg.MaxInflightPerToken != 3 {
		t.Errorf("GATEWAY_MAX_INFLIGHT_PER_TOKEN=3: got %d, %v", cfg.MaxInflightPerToken, err)
	}
	if cfg, err = gatewayConfig(envOf(nil)); err != nil || cfg.MaxInflightPerToken != 0 {
		t.Errorf("unset: got %d, %v; want 0 (gateway default)", cfg.MaxInflightPerToken, err)
	}
	for _, bad := range []string{"0", "-1", "eight"} {
		if _, err := gatewayConfig(envOf(map[string]string{"GATEWAY_MAX_INFLIGHT_PER_TOKEN": bad})); err == nil ||
			!strings.Contains(err.Error(), "GATEWAY_MAX_INFLIGHT_PER_TOKEN") {
			t.Errorf("GATEWAY_MAX_INFLIGHT_PER_TOKEN=%q: err %v, want one naming the variable", bad, err)
		}
	}
}
