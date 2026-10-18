// Command gateway is the factory's model gateway (SPEC.md §7.3).
//
//	gateway [serve]                                   run the proxy (default)
//	gateway mint-token --run R [--task T] --stage S [--ttl 1h]   (ttl at most 2h)
//	gateway revoke-token --run R                      revoke every token of a run
//
// Environment (serve):
//
//	ANTHROPIC_KEY_SECRET_ID  Secrets Manager secret holding the key (required)
//	DATABASE_URL             Postgres DSN; empty falls back to libpq PG* variables
//	LISTEN_ADDR              proxy, /healthz and /readyz (default :8080)
//	METRICS_ADDR             /metrics (default :9090)
//	UPSTREAM_URL             default https://api.anthropic.com
//	GATEWAY_ALLOW_INSECURE_UPSTREAM  "1" allows an http:// UPSTREAM_URL (tests only)
//	GATEWAY_MAX_INFLIGHT_PER_TOKEN  concurrent requests per job token (default 8)
//	AWS_REGION and the rest of the AWS SDK default credential chain
//
// mint-token and revoke-token use DATABASE_URL only. mint-token prints
// nothing but the token on stdout; revoke-token prints the number of tokens
// it revoked (0 is not an error).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/LinuxAd/dark-factory/orchestrator/internal/gateway"
	"github.com/LinuxAd/dark-factory/orchestrator/internal/secrets"
	"github.com/LinuxAd/dark-factory/orchestrator/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		log := slog.New(slog.NewJSONHandler(stdout, nil))
		if err := serve(ctx, getenv, log); err != nil {
			log.Error("gateway stopped", "error", err.Error())
			return 1
		}
		return 0
	case "mint-token":
		if err := mintToken(ctx, args, getenv, stdout, stderr); err != nil {
			_, _ = fmt.Fprintln(stderr, "mint-token:", err)
			return 1
		}
		return 0
	case "revoke-token":
		if err := revokeToken(ctx, args, getenv, stdout, stderr); err != nil {
			_, _ = fmt.Fprintln(stderr, "revoke-token:", err)
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q (want serve, mint-token or revoke-token)\n", cmd)
		return 2
	}
}

func envOr(getenv func(string) string, k, def string) string {
	if v := getenv(k); v != "" {
		return v
	}
	return def
}

// gatewayConfig reads the gateway settings that come from the environment.
func gatewayConfig(getenv func(string) string) (gateway.Config, error) {
	var cfg gateway.Config
	upstream, err := url.Parse(envOr(getenv, "UPSTREAM_URL", "https://api.anthropic.com"))
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return cfg, fmt.Errorf("UPSTREAM_URL is not an absolute URL")
	}
	// The upstream receives the API key, so plain http is refused unless a
	// test or local setup opts in explicitly.
	insecureOK := getenv("GATEWAY_ALLOW_INSECURE_UPSTREAM") == "1"
	if upstream.Scheme != "https" && (!insecureOK || upstream.Scheme != "http") {
		return cfg, fmt.Errorf("UPSTREAM_URL must be https (http only with GATEWAY_ALLOW_INSECURE_UPSTREAM=1), got scheme %q", upstream.Scheme)
	}
	cfg.Upstream = upstream
	if v := getenv("GATEWAY_MAX_INFLIGHT_PER_TOKEN"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("GATEWAY_MAX_INFLIGHT_PER_TOKEN must be a positive integer, got %q", v)
		}
		cfg.MaxInflightPerToken = n
	}
	return cfg, nil
}

func serve(ctx context.Context, getenv func(string) string, log *slog.Logger) error {
	cfg, err := gatewayConfig(getenv)
	if err != nil {
		return err
	}
	secretID := getenv("ANTHROPIC_KEY_SECRET_ID")
	if secretID == "" {
		return errors.New("ANTHROPIC_KEY_SECRET_ID is required")
	}
	keys, err := secrets.NewManager(ctx, secretID)
	if err != nil {
		return err
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	cfg.Logger, cfg.Registerer = log, reg
	gw := gateway.New(cfg)
	defer gw.Close()

	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	servers := []*http.Server{
		newHTTPServer(envOr(getenv, "LISTEN_ADDR", ":8080"), gw.Handler()),
		newHTTPServer(envOr(getenv, "METRICS_ADDR", ":9090"), metricsMux),
	}
	errc := make(chan error, len(servers)+1)
	for _, s := range servers {
		log.Info("listening", "addr", s.Addr)
		go func() {
			if err := s.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("listen %s: %w", s.Addr, err)
			}
		}()
	}
	go func() {
		if err := gw.Init(ctx, keys, getenv("DATABASE_URL")); err != nil {
			errc <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// newHTTPServer sets the connection limits shared by both listeners. There is
// no write timeout: streamed responses can run for many minutes.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// maxTokenTTL caps a job token's lifetime so a leaked token is short-lived.
const maxTokenTTL = 2 * time.Hour

func mintToken(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("mint-token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	run := fs.String("run", "", "run id (required)")
	task := fs.String("task", "", "task id")
	stage := fs.String("stage", "", "stage (required)")
	ttl := fs.Duration("ttl", time.Hour, "token lifetime")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *run == "" || *stage == "" {
		return errors.New("--run and --stage are required")
	}
	if *ttl <= 0 || *ttl > maxTokenTTL {
		return fmt.Errorf("--ttl must be positive and at most %s", maxTokenTTL)
	}
	st, err := store.Open(ctx, getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer st.Close()
	tok, hash, err := gateway.MintToken()
	if err != nil {
		return err
	}
	if err := st.CreateToken(ctx, hash, store.Token{RunID: *run, TaskID: *task, Stage: *stage, ExpiresAt: time.Now().Add(*ttl)}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, tok)
	return err
}

func revokeToken(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("revoke-token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	run := fs.String("run", "", "run id whose tokens to revoke (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *run == "" {
		return errors.New("--run is required")
	}
	st, err := store.Open(ctx, getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := st.RevokeRunTokens(ctx, *run)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, n)
	return err
}
