// Package gateway is the factory's model gateway (SPEC.md §7.3): it holds
// the only Anthropic credential, authenticates callers by job token, forwards
// requests untranslated (streaming included) and writes one agent_calls row
// per upstream POST /v1/messages request.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/LinuxAd/dark-factory/orchestrator/internal/secrets"
	"github.com/LinuxAd/dark-factory/orchestrator/internal/store"
)

// maxBackoff caps the startup retry delay; a CNPG recovery takes minutes.
const maxBackoff = 30 * time.Second

// Config holds the gateway's dependencies. Zero values get defaults in New.
type Config struct {
	Upstream    *url.URL
	Logger      *slog.Logger
	Registerer  prometheus.Registerer
	Budget      Budget
	Transport   http.RoundTripper
	BackoffBase time.Duration // first retry delay, default 1s
	Now         func() time.Time
}

// Server is one gateway instance.
type Server struct {
	cfg     Config
	log     *slog.Logger
	proxy   *httputil.ReverseProxy
	metrics *metrics

	ready atomic.Bool
	key   string       // written once in Init, read only after ready is set
	store *store.Store // likewise
}

// New builds a Server that is not ready until Init succeeds.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Registerer == nil {
		cfg.Registerer = prometheus.NewRegistry()
	}
	if cfg.Budget == nil {
		cfg.Budget = NoBudget{}
	}
	if cfg.Transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		// Never ask upstream for compressed bodies, and never let the
		// transport add Accept-Encoding itself: the usage parser reads
		// plain bytes (Decision D).
		t.DisableCompression = true
		cfg.Transport = t
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Server{cfg: cfg, log: cfg.Logger, metrics: newMetrics(cfg.Registerer)}
	s.proxy = &httputil.ReverseProxy{
		Rewrite:        s.rewrite,
		Transport:      cfg.Transport,
		ModifyResponse: s.modifyResponse,
		ErrorHandler:   s.proxyError,
		ErrorLog:       log.New(slogWriter{s.log}, "", 0),
	}
	return s
}

// Init fetches the key, connects to Postgres and runs migrations, retrying
// each step with exponential backoff until it succeeds or ctx ends. The
// server reports ready only after all three.
func (s *Server) Init(ctx context.Context, keys secrets.KeySource, dsn string) error {
	key, err := retry(ctx, s, "fetch API key", keys.FetchKey)
	if err != nil {
		return err
	}
	st, err := retry(ctx, s, "connect to database", func(ctx context.Context) (*store.Store, error) {
		return store.Open(ctx, dsn)
	})
	if err != nil {
		return err
	}
	if _, err := retry(ctx, s, "run migrations", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, st.Migrate(ctx)
	}); err != nil {
		st.Close()
		return err
	}
	s.key, s.store = key, st
	s.ready.Store(true)
	s.log.Info("gateway ready")
	return nil
}

// Ready reports whether Init has finished.
func (s *Server) Ready() bool { return s.ready.Load() }

// Close releases the database pool.
func (s *Server) Close() {
	if s.store != nil {
		s.store.Close()
	}
}

// backoff returns the delay before retry attempt n (0-based).
func backoff(base time.Duration, n int) time.Duration {
	d := base
	for range n {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}
	return min(d, maxBackoff)
}

func retry[T any](ctx context.Context, s *Server, what string, f func(context.Context) (T, error)) (T, error) {
	for n := 0; ; n++ {
		v, err := f(ctx)
		if err == nil {
			return v, nil
		}
		d := backoff(s.cfg.BackoffBase, n)
		s.log.Warn("startup step failed, retrying", "step", what, "attempt", n+1, "retry_in", d.String(), "error", err.Error())
		select {
		case <-ctx.Done():
			var zero T
			return zero, fmt.Errorf("%s: %w", what, ctx.Err())
		case <-time.After(d):
		}
	}
}

// Handler serves /healthz, /readyz and the proxy. It dispatches on the raw
// request path rather than through http.ServeMux, which would clean paths
// and redirect, so the allowlist sees exactly what the client sent.
func (s *Server) Handler() http.Handler {
	proxy := s.instrument(http.HandlerFunc(s.serveProxy))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/healthz" && r.URL.RawPath == "":
			_, _ = w.Write([]byte("ok\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/readyz" && r.URL.RawPath == "":
			if !s.Ready() {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte("ready\n"))
		default:
			proxy.ServeHTTP(w, r)
		}
	})
}

// route reports whether a request is on the forwarding allowlist (Decision
// D) and whether it is metered. Batches, Files and every other endpoint are
// refused: they would be unmetered and act workspace-wide. Escaped paths
// (non-empty RawPath) are refused outright so no encoding can smuggle a
// different path past the comparison.
func route(r *http.Request) (allowed, metered bool) {
	if r.URL.RawPath != "" {
		return false, false
	}
	p := r.URL.Path
	switch r.Method {
	case http.MethodPost:
		switch p {
		case "/v1/messages":
			return true, true
		case "/v1/messages/count_tokens":
			return true, false
		}
	case http.MethodGet:
		if p == "/v1/models" {
			return true, false
		}
		if id, ok := strings.CutPrefix(p, "/v1/models/"); ok && validModelID(id) {
			return true, false
		}
	}
	return false, false
}

// validModelID accepts one path segment of letters, digits, '.', '_' and
// '-' that is not "." or "..".
func validModelID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, c := range id {
		ok := c == '.' || c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

// reqInfo is filled in while a request is served and read by instrument.
type reqInfo struct {
	caller  Caller
	model   string
	proxied bool
	metered bool
	start   time.Time
}

type ctxKey struct{}

func info(r *http.Request) *reqInfo {
	v, _ := r.Context().Value(ctxKey{}).(*reqInfo)
	return v
}

func (s *Server) serveProxy(w http.ResponseWriter, r *http.Request) {
	ri := info(r)
	allowed, metered := route(r)
	if !allowed {
		writeError(w, http.StatusNotFound, "not_found_error", "not found")
		return
	}
	if !s.Ready() {
		writeError(w, http.StatusServiceUnavailable, "api_error", "gateway is starting")
		return
	}
	tok, ok := bearerToken(r.Header)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_error", "missing bearer token")
		return
	}
	t, err := s.store.LookupToken(r.Context(), HashToken(tok))
	if errors.Is(err, store.ErrTokenNotFound) {
		writeError(w, http.StatusUnauthorized, "authentication_error", errTokenInvalid.Error())
		return
	}
	if err != nil {
		s.log.Error("token lookup failed", "error", err.Error())
		writeError(w, http.StatusServiceUnavailable, "api_error", "token lookup failed")
		return
	}
	c, err := checkToken(t, r.Header, s.cfg.Now())
	switch {
	case errors.Is(err, errHeaderMismatch):
		writeError(w, http.StatusForbidden, "permission_error", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusUnauthorized, "authentication_error", err.Error())
		return
	}
	ri.caller = c
	if err := s.cfg.Budget.Check(r.Context(), c); err != nil {
		writeError(w, http.StatusTooManyRequests, "rate_limit_error", "budget exceeded: "+err.Error())
		return
	}
	ri.proxied = true
	ri.metered = metered
	s.proxy.ServeHTTP(w, r)
}

func (s *Server) rewrite(pr *httputil.ProxyRequest) {
	pr.SetURL(s.cfg.Upstream)
	h := pr.Out.Header
	h.Del("Authorization")
	h.Del("X-Api-Key")
	h.Del("Accept-Encoding")
	for name := range h {
		if strings.HasPrefix(name, "X-Factory-") {
			h.Del(name)
		}
	}
	h.Set("X-Api-Key", s.key)
}

func (s *Server) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	ri := info(r)
	status := http.StatusBadGateway
	if r.Context().Err() != nil {
		status = statusClientClosed
	}
	if ri.metered {
		s.record(r.Context(), ri, zeroUsage, status)
	}
	if status == statusClientClosed {
		return
	}
	s.log.Warn("upstream request failed", "path", r.URL.Path, "error", err.Error())
	writeError(w, http.StatusBadGateway, "api_error", "upstream request failed")
}

// instrument records the request metric, upstream latency and one log line.
// It never logs headers or bodies.
func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ri := &reqInfo{start: time.Now()}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, ri))
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			p := recover()
			code := sw.code()
			if p != nil || r.Context().Err() != nil {
				code = statusClientClosed
			}
			elapsed := time.Since(ri.start)
			s.metrics.requests.WithLabelValues(fmt.Sprint(code)).Inc()
			if ri.proxied {
				s.metrics.upstreamLatency.Observe(elapsed.Seconds())
			}
			s.log.Info("request",
				"method", r.Method, "path", r.URL.Path, "status", code,
				"latency_ms", elapsed.Milliseconds(),
				"run_id", ri.caller.RunID, "task_id", ri.caller.TaskID, "stage", ri.caller.Stage,
				"model", ri.model)
			if p != nil {
				panic(p) // http.ErrAbortHandler from ReverseProxy on a broken copy
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// statusClientClosed is the nginx convention for a client that went away.
const statusClientClosed = 499

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush on the real writer, which
// ReverseProxy needs to stream text/event-stream.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func writeError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": typ, "message": msg},
	})
}

// slogWriter routes ReverseProxy's internal log lines to slog.
type slogWriter struct{ l *slog.Logger }

func (w slogWriter) Write(b []byte) (int, error) {
	w.l.Warn("reverse proxy", "detail", strings.TrimSpace(string(b)))
	return len(b), nil
}

type metrics struct {
	requests        *prometheus.CounterVec
	upstreamLatency prometheus.Histogram
	unpriced        prometheus.Counter
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_requests_total",
			Help: "Proxied requests by response status code (499: client went away).",
		}, []string{"code"}),
		upstreamLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "gateway_upstream_latency_seconds",
			Help:    "Time from receiving a forwarded request to the end of its response body.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
		}),
		unpriced: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gateway_unpriced_calls_total",
			Help: "agent_calls rows written with NULL cost because the model has no price.",
		}),
	}
	reg.MustRegister(m.requests, m.upstreamLatency, m.unpriced)
	return m
}
