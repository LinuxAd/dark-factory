package gateway

// Integration tests for AC2 (a)-(j): a real gateway in front of an httptest
// fake upstream replaying the internal/usage fixtures, with real Postgres
// (see internal/testpg). Expected token counts and costs are worked out by
// hand from the fixture files and the published prices, not by calling the
// code under test.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/LinuxAd/dark-factory/orchestrator/internal/secrets"
	"github.com/LinuxAd/dark-factory/orchestrator/internal/store"
	"github.com/LinuxAd/dark-factory/orchestrator/internal/testpg"
)

const testKey = "sk-ant-api03-TESTKEY-must-never-appear-in-logs-0123456789"

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type seenRequest struct {
	method, path string
	header       http.Header
	body         []byte
}

// upstream is the fake Anthropic API.
type upstream struct {
	mu      sync.Mutex
	seen    []seenRequest
	handler http.HandlerFunc
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.seen = append(u.seen, seenRequest{r.Method, r.URL.Path, r.Header.Clone(), body})
	u.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(body))
	u.handler(w, r)
}

func (u *upstream) requests() []seenRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]seenRequest(nil), u.seen...)
}

type env struct {
	srv   *Server
	front *httptest.Server
	up    *upstream
	upSrv *httptest.Server
	st    *store.Store
	dsn   string
	logs  *syncBuf
	reg   *prometheus.Registry
}

func newEnv(t *testing.T, h http.HandlerFunc) *env {
	t.Helper()
	dsn := testpg.New(t)
	e := &env{dsn: dsn, up: &upstream{handler: h}, logs: &syncBuf{}, reg: prometheus.NewRegistry()}
	e.upSrv = httptest.NewServer(e.up)
	t.Cleanup(e.upSrv.Close)
	u, _ := url.Parse(e.upSrv.URL)
	e.srv = New(Config{
		Upstream:    u,
		Logger:      slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Registerer:  e.reg,
		BackoffBase: 10 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := e.srv.Init(ctx, secrets.Static(testKey), dsn); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(e.srv.Close)
	e.front = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.front.Close)
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e.st = st
	return e
}

func (e *env) token(t *testing.T, tok store.Token) string {
	t.Helper()
	if tok.ExpiresAt.IsZero() {
		tok.ExpiresAt = time.Now().Add(time.Hour)
	}
	s, hash, err := MintToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateToken(context.Background(), hash, tok); err != nil {
		t.Fatal(err)
	}
	return s
}

type row struct {
	model                                  string
	task                                   *string
	in, out, cacheRead, cacheWrite, server int64
	cost                                   *string
	latency, status                        int64
}

func (e *env) rows(t *testing.T, run string) []row {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, e.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rs, err := conn.Query(ctx, `SELECT model, task_id, input_tokens, output_tokens, cache_read_tokens,
		cache_write_tokens, server_tool_requests, cost_usd::text, latency_ms, http_status
		FROM agent_calls WHERE run_id = $1 ORDER BY id`, run)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.model, &r.task, &r.in, &r.out, &r.cacheRead, &r.cacheWrite, &r.server, &r.cost, &r.latency, &r.status); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// waitRows polls because a row may be written just after the client has
// read the last byte.
func (e *env) waitRows(t *testing.T, run string, n int) []row {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rs := e.rows(t, run)
		if len(rs) >= n || time.Now().After(deadline) {
			if len(rs) != n {
				t.Fatalf("run %s: got %d rows, want %d", run, len(rs), n)
			}
			return rs
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "usage", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// serveFixture replays a fixture. SSE fixtures are written one event at a
// time with a flush after each, like the real API.
func serveFixture(t *testing.T, name string) http.HandlerFunc {
	b := fixture(t, name)
	return func(w http.ResponseWriter, _ *http.Request) {
		if !strings.HasSuffix(name, ".sse") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		f := w.(http.Flusher)
		for _, ev := range bytes.SplitAfter(b, []byte("\n\n")) {
			_, _ = w.Write(ev)
			f.Flush()
		}
	}
}

func post(t *testing.T, e *env, path, tok string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.front.URL+path, strings.NewReader(`{"model":"claude-opus-5-5","max_tokens":16,"messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func strp(s string) *string { return &s }

func assertRow(t *testing.T, got, want row) {
	t.Helper()
	if got.latency < 0 {
		t.Errorf("negative latency %d", got.latency)
	}
	got.latency, want.latency = 0, 0
	eq := func(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
	if got.model != want.model || got.in != want.in || got.out != want.out || got.cacheRead != want.cacheRead ||
		got.cacheWrite != want.cacheWrite || got.server != want.server || got.status != want.status ||
		!eq(got.cost, want.cost) || !eq(got.task, want.task) {
		show := func(p *string) string {
			if p == nil {
				return "NULL"
			}
			return *p
		}
		t.Errorf("row mismatch\n got: %+v cost=%s task=%s\nwant: %+v cost=%s task=%s",
			got, show(got.cost), show(got.task), want, show(want.cost), show(want.task))
	}
}

// AC2 (a): non-stream response -> one row with exact tokens.
func TestNonStreamOneRowExactTokens(t *testing.T) {
	e := newEnv(t, serveFixture(t, "message.json"))
	tok := e.token(t, store.Token{RunID: "run-a", TaskID: "task-a", Stage: "plan"})
	resp, body := post(t, e, "/v1/messages", tok, nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, fixture(t, "message.json")) {
		t.Fatalf("status %d, body differs from upstream: %q", resp.StatusCode, body)
	}
	rows := e.waitRows(t, "run-a", 1)
	// Sonnet 5.5: 1520*$2 + 42*$10 + 10240*$0.20 + 2048*$2.50 per MTok = $0.010628
	assertRow(t, rows[0], row{model: "claude-sonnet-5-5", task: strp("task-a"), in: 1520, out: 42,
		cacheRead: 10240, cacheWrite: 2048, cost: strp("0.010628"), status: 200})
	if got := testutil.ToFloat64(e.srv.metrics.requests.WithLabelValues("200")); got != 1 {
		t.Errorf("gateway_requests_total{code=200} = %v, want 1", got)
	}
}

// AC2 (b): stream -> identical bytes; input/cache from message_start, output
// from the last message_delta.
func TestStreamIdenticalBytesLastValueUsage(t *testing.T) {
	e := newEnv(t, serveFixture(t, "stream.sse"))
	tok := e.token(t, store.Token{RunID: "run-b", Stage: "build"})
	resp, body := post(t, e, "/v1/messages?beta=true", tok, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !bytes.Equal(body, fixture(t, "stream.sse")) {
		t.Fatalf("client bytes differ from upstream bytes:\n%s", body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type %q", ct)
	}
	rows := e.waitRows(t, "run-b", 1)
	// Opus 5.5: 812*$4 + 87*$20 + 4096*$0.20 + 1024*$5 per MTok = $0.0109272
	assertRow(t, rows[0], row{model: "claude-opus-5-5", in: 812, out: 87, cacheRead: 4096, cacheWrite: 1024,
		cost: strp("0.010927"), status: 200})
}

func TestStreamServerToolUsage(t *testing.T) {
	e := newEnv(t, serveFixture(t, "stream_server_tool.sse"))
	tok := e.token(t, store.Token{RunID: "run-tool", Stage: "research"})
	post(t, e, "/v1/messages", tok, nil)
	rows := e.waitRows(t, "run-tool", 1)
	// 9850*$4 + 215*$20 + 512*$0.20 per MTok + 2 web searches * $0.01 = $0.0638024
	assertRow(t, rows[0], row{model: "claude-opus-5-5", in: 9850, out: 215, cacheRead: 512, server: 3,
		cost: strp("0.063802"), status: 200})
}

func TestStreamErrorEventStillRecorded(t *testing.T) {
	e := newEnv(t, serveFixture(t, "stream_error.sse"))
	tok := e.token(t, store.Token{RunID: "run-err", Stage: "build"})
	_, body := post(t, e, "/v1/messages", tok, nil)
	if !bytes.Equal(body, fixture(t, "stream_error.sse")) {
		t.Fatal("client bytes differ")
	}
	rows := e.waitRows(t, "run-err", 1)
	// Haiku 5.5 short prompt: 300*$0.10 + 1*$0.50 per MTok = $0.0000305
	assertRow(t, rows[0], row{model: "claude-haiku-5-5", in: 300, out: 1, cost: strp("0.000031"), status: 200})
}

func TestUpstreamErrorStatusRecordedAtZeroCost(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	})
	tok := e.token(t, store.Token{RunID: "run-400", Stage: "build"})
	resp, _ := post(t, e, "/v1/messages", tok, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	rows := e.waitRows(t, "run-400", 1)
	assertRow(t, rows[0], row{cost: strp("0.000000"), status: 400})
}

func TestUnknownModelNullCostAndCounter(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","model":"claude-future-9","usage":{"input_tokens":10,"output_tokens":2}}`))
	})
	tok := e.token(t, store.Token{RunID: "run-unpriced", Stage: "build"})
	resp, _ := post(t, e, "/v1/messages", tok, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("unknown model must not fail the call: status %d", resp.StatusCode)
	}
	rows := e.waitRows(t, "run-unpriced", 1)
	assertRow(t, rows[0], row{model: "claude-future-9", in: 10, out: 2, cost: nil, status: 200})
	if got := testutil.ToFloat64(e.srv.metrics.unpriced); got != 1 {
		t.Errorf("gateway_unpriced_calls_total = %v, want 1", got)
	}
}

// AC2 (c): no, invalid, expired or revoked token -> 401 and no upstream call.
func TestUnauthenticatedRejectedWithoutUpstreamCall(t *testing.T) {
	e := newEnv(t, serveFixture(t, "message.json"))
	expired := e.token(t, store.Token{RunID: "run-c", Stage: "s", ExpiresAt: time.Now().Add(-time.Second)})
	revoked := e.token(t, store.Token{RunID: "run-c", Stage: "s"})
	conn, err := pgx.Connect(context.Background(), e.dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), `UPDATE gateway_tokens SET revoked_at = now() WHERE token_sha256 = $1`, HashToken(revoked)); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(context.Background())

	cases := map[string]map[string]string{
		"no token":       nil,
		"invalid token":  {"Authorization": "Bearer dft_not-a-real-token"},
		"expired token":  {"Authorization": "Bearer " + expired},
		"revoked token":  {"Authorization": "Bearer " + revoked},
		"x-api-key only": {"X-Api-Key": "sk-ant-client-supplied"},
	}
	for name, hdr := range cases {
		resp, body := post(t, e, "/v1/messages", "", hdr)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
		if !bytes.Contains(body, []byte(`"authentication_error"`)) {
			t.Errorf("%s: body %s", name, body)
		}
	}
	if n := len(e.up.requests()); n != 0 {
		t.Errorf("upstream received %d requests, want 0", n)
	}
	if rows := e.rows(t, "run-c"); len(rows) != 0 {
		t.Errorf("got %d rows for rejected requests", len(rows))
	}
}

// AC2 (d): token plus mismatching X-Factory-Run -> 403.
func TestHeaderTokenMismatchForbidden(t *testing.T) {
	e := newEnv(t, serveFixture(t, "message.json"))
	tok := e.token(t, store.Token{RunID: "run-d", TaskID: "t1", Stage: "build"})
	resp, _ := post(t, e, "/v1/messages", tok, map[string]string{"X-Factory-Run": "someone-else"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if n := len(e.up.requests()); n != 0 {
		t.Errorf("upstream received %d requests, want 0", n)
	}
	// Matching headers are fine.
	resp, _ = post(t, e, "/v1/messages", tok, map[string]string{"X-Factory-Run": "run-d", "X-Factory-Task": "t1", "X-Factory-Stage": "build"})
	if resp.StatusCode != 200 {
		t.Fatalf("matching headers: status %d", resp.StatusCode)
	}
}

// AC2 (e): inbound x-api-key, authorization and accept-encoding never reach
// upstream; everything else is forwarded untouched.
func TestCredentialHeadersStripped(t *testing.T) {
	e := newEnv(t, serveFixture(t, "message.json"))
	tok := e.token(t, store.Token{RunID: "run-e", Stage: "build"})
	resp, _ := post(t, e, "/v1/messages", tok, map[string]string{
		"X-Api-Key":         "sk-ant-client-supplied",
		"Accept-Encoding":   "gzip, br",
		"Anthropic-Beta":    "context-management-2025-06-27",
		"Anthropic-Version": "2023-06-01",
		"X-Factory-Run":     "run-e",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	reqs := e.up.requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream got %d requests", len(reqs))
	}
	h := reqs[0].header
	if got := h.Values("X-Api-Key"); len(got) != 1 || got[0] != testKey {
		t.Errorf("upstream x-api-key = %q, want only the gateway key", got)
	}
	for _, name := range []string{"Authorization", "Accept-Encoding", "X-Factory-Run"} {
		if v := h.Values(name); len(v) != 0 {
			t.Errorf("upstream received %s: %q", name, v)
		}
	}
	if h.Get("Anthropic-Beta") != "context-management-2025-06-27" || h.Get("Anthropic-Version") != "2023-06-01" {
		t.Errorf("anthropic headers not forwarded: %v", h)
	}
	if !bytes.Equal(reqs[0].body, []byte(`{"model":"claude-opus-5-5","max_tokens":16,"messages":[]}`)) {
		t.Errorf("body changed: %s", reqs[0].body)
	}
	if reqs[0].path != "/v1/messages" {
		t.Errorf("path %q", reqs[0].path)
	}
}

// AC2 (f): a gzip-capable client still gets correct usage. The fake upstream
// compresses whenever asked, as the real API does.
func TestGzipCapableClientStillMetered(t *testing.T) {
	plain := serveFixture(t, "stream.sse")
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "text/event-stream")
			gz := gzip.NewWriter(w)
			_, _ = gz.Write(fixture(t, "stream.sse"))
			_ = gz.Close()
			return
		}
		plain(w, r)
	})
	tok := e.token(t, store.Token{RunID: "run-f", Stage: "build"})
	// Go's default client sends Accept-Encoding: gzip implicitly; also send
	// it explicitly from a client that would not decode it for us.
	resp, body := post(t, e, "/v1/messages", tok, map[string]string{"Accept-Encoding": "gzip"})
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Encoding") != "" || !bytes.Equal(body, fixture(t, "stream.sse")) {
		t.Fatalf("client did not receive the plain stream (encoding %q)", resp.Header.Get("Content-Encoding"))
	}
	rows := e.waitRows(t, "run-f", 1)
	if rows[0].in != 812 || rows[0].out != 87 || rows[0].cacheRead != 4096 {
		t.Errorf("usage wrong behind gzip-capable client: %+v", rows[0])
	}
}

// AC2 (g): client cancels mid-stream -> row with http_status 499.
func TestClientCancelMidStreamRecords499(t *testing.T) {
	events := bytes.SplitAfter(fixture(t, "stream.sse"), []byte("\n\n"))
	upstreamDone := make(chan struct{})
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events[:4] { // message_start .. first content_block_delta
			_, _ = w.Write(ev)
			w.(http.Flusher).Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
		}
	})
	tok := e.token(t, store.Token{RunID: "run-g", Stage: "build"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.front.URL+"/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// Streaming must be live: the first event arrives while upstream is
	// still holding the response open.
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "content_block_delta") {
			break
		}
	}
	cancel()
	_ = resp.Body.Close()
	select {
	case <-upstreamDone:
	case <-time.After(15 * time.Second):
		t.Fatal("upstream request was not cancelled")
	}
	rows := e.waitRows(t, "run-g", 1)
	// Only message_start was seen: 812*$4 + 1*$20 + 4096*$0.20 + 1024*$5 per MTok = $0.0092072
	assertRow(t, rows[0], row{model: "claude-opus-5-5", in: 812, out: 1, cacheRead: 4096, cacheWrite: 1024,
		cost: strp("0.009207"), status: 499})
}

// AC2 (h): count_tokens (and other paths) pass through and write no row.
func TestNonMessagesPathsPassThroughWithoutRow(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages/count_tokens":
			serveFixture(t, "count_tokens.json")(w, r)
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-5-5","type":"model"}],"has_more":false}`))
		default:
			serveFixture(t, "message.json")(w, r)
		}
	})
	tok := e.token(t, store.Token{RunID: "run-h", Stage: "build"})
	resp, body := post(t, e, "/v1/messages/count_tokens", tok, nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, fixture(t, "count_tokens.json")) {
		t.Fatalf("count_tokens: status %d body %s", resp.StatusCode, body)
	}
	req, _ := http.NewRequest(http.MethodGet, e.front.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mbody, _ := io.ReadAll(mresp.Body)
	_ = mresp.Body.Close()
	if mresp.StatusCode != 200 || !bytes.Contains(mbody, []byte("claude-opus-5-5")) {
		t.Fatalf("/v1/models: status %d body %s", mresp.StatusCode, mbody)
	}
	// A row is written before the handler returns, so after this messages
	// call its row must be the only one for the run.
	post(t, e, "/v1/messages", tok, nil)
	if rows := e.waitRows(t, "run-h", 1); rows[0].status != 200 || rows[0].in != 1520 {
		t.Errorf("unexpected row %+v", rows[0])
	}
	if n := len(e.up.requests()); n != 3 {
		t.Errorf("upstream got %d requests, want 3", n)
	}
}

// do sends a bodyless request with a bearer token. rawURL is appended to the
// gateway URL unparsed, so escaped paths reach the server as written.
func do(t *testing.T, e *env, method, rawURL, tok string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.front.URL+rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

// Only the four allowlisted routes are forwarded; Batches, Files and any
// other path or method get 404 with no upstream call and no row.
func TestPathAllowlist(t *testing.T) {
	e := newEnv(t, serveFixture(t, "message.json"))
	tok := e.token(t, store.Token{RunID: "run-allow", Stage: "build"})
	denied := []struct{ method, url string }{
		{"POST", "/v1/messages/batches"},
		{"GET", "/v1/messages/batches"},
		{"GET", "/v1/files"},
		{"POST", "/v1/files"},
		{"POST", "/v1/messages/"},
		{"POST", "/v1//messages"},
		{"POST", "/v1/messages/count_tokens/"},
		{"GET", "/v1/models/"},
		{"GET", "/v1/models/a/b"},
		{"GET", "/v1/models/.."},
		{"GET", "/v1/models/claude%2Dopus-5-5"}, // non-empty RawPath
		{"GET", "/v1/models/a%2Fb"},
		{"GET", "/v1/messages"},
		{"POST", "/v1/models"},
		{"DELETE", "/v1/models/claude-opus-5-5"},
		{"GET", "/v1/organizations/me"},
		{"GET", "/"},
	}
	for _, c := range denied {
		resp, body := do(t, e, c.method, c.url, tok)
		if resp.StatusCode != http.StatusNotFound || !bytes.Contains(body, []byte(`"not_found_error"`)) {
			t.Errorf("%s %s: status %d body %s, want 404 not_found_error", c.method, c.url, resp.StatusCode, body)
		}
	}
	if n := len(e.up.requests()); n != 0 {
		t.Errorf("upstream received %d requests for denied paths, want 0", n)
	}
	if rows := e.rows(t, "run-allow"); len(rows) != 0 {
		t.Errorf("got %d rows for denied paths", len(rows))
	}
	for _, c := range []struct{ method, url string }{
		{"GET", "/v1/models"},
		{"GET", "/v1/models/claude-opus-5-5"},
		{"POST", "/v1/messages/count_tokens"},
	} {
		if resp, _ := do(t, e, c.method, c.url, tok); resp.StatusCode != 200 {
			t.Errorf("%s %s: status %d, want 200", c.method, c.url, resp.StatusCode)
		}
	}
	if n := len(e.up.requests()); n != 3 {
		t.Errorf("upstream received %d requests for allowed paths, want 3", n)
	}
}

// AC2 (i): captured logs never contain the configured key (or job tokens).
func TestLogsNeverContainKey(t *testing.T) {
	var mode atomic.Value
	mode.Store("json")
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case "echo":
			// A hostile upstream echoing request headers in its body and
			// response headers.
			w.Header().Set("X-Echo-Key", r.Header.Get("X-Api-Key"))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"` + r.Header.Get("X-Api-Key") + `"}}`))
		case "stream":
			serveFixture(t, "stream.sse")(w, r)
		default:
			serveFixture(t, "message.json")(w, r)
		}
	})
	tok := e.token(t, store.Token{RunID: "run-i", Stage: "build"})
	post(t, e, "/v1/messages", tok, nil)
	mode.Store("stream")
	post(t, e, "/v1/messages", tok, nil)
	mode.Store("echo")
	post(t, e, "/v1/messages", tok, nil)
	post(t, e, "/v1/messages", "dft_bogus", nil)
	post(t, e, "/v1/messages", tok, map[string]string{"X-Factory-Run": "other"})
	e.upSrv.Close() // upstream unreachable -> 502 path logs the transport error
	resp, _ := post(t, e, "/v1/messages", tok, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("unreachable upstream: status %d, want 502", resp.StatusCode)
	}
	e.waitRows(t, "run-i", 4)

	logs := e.logs.String()
	if !strings.Contains(logs, `"msg":"request"`) {
		t.Fatalf("no request log lines captured:\n%s", logs)
	}
	for _, secret := range []string{testKey, tok, "dft_bogus"} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs contain a secret (%.12s...):\n%s", secret, logs)
		}
	}
	if strings.Contains(strings.ToLower(logs), "authorization") {
		t.Errorf("logs mention a header:\n%s", logs)
	}
}

// AC2 (j): two gateways starting at once both come up, migrations ran once.
func TestConcurrentStartupMigratesOnce(t *testing.T) {
	dsn := testpg.New(t)
	u, _ := url.Parse("http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	servers := make([]*Server, 2)
	errs := make([]error, 2)
	for i := range servers {
		servers[i] = New(Config{Upstream: u, Logger: slog.New(slog.DiscardHandler), BackoffBase: 10 * time.Millisecond})
		wg.Go(func() { errs[i] = servers[i].Init(ctx, secrets.Static(testKey), dsn) })
	}
	wg.Wait()
	for i, s := range servers {
		if errs[i] != nil || !s.Ready() {
			t.Fatalf("gateway %d: ready=%v err=%v", i, s.Ready(), errs[i])
		}
		defer s.Close()
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rs, err := conn.Query(ctx, `SELECT version_id, count(*) FROM goose_db_version WHERE version_id > 0 GROUP BY version_id ORDER BY version_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var versions []int64
	for rs.Next() {
		var v, n int64
		if err := rs.Scan(&v, &n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("migration %d applied %d times", v, n)
		}
		versions = append(versions, v)
	}
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Errorf("applied versions %v, want [1 2]", versions)
	}
}

func TestReadinessAndHealth(t *testing.T) {
	dsn := testpg.New(t)
	u, _ := url.Parse("http://127.0.0.1:1")
	s := New(Config{Upstream: u, Logger: slog.New(slog.DiscardHandler), BackoffBase: time.Millisecond})
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	get := func(path string) int {
		resp, err := http.Get(h.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if get("/healthz") != 200 || get("/readyz") != 503 || get("/v1/models") != 503 {
		t.Fatal("before Init: want healthz 200, readyz 503, proxy 503")
	}
	// The key source fails twice; Init retries with backoff and succeeds.
	var calls atomic.Int32
	keys := keyFunc(func(context.Context) (string, error) {
		if calls.Add(1) <= 2 {
			return "", errors.New("AccessDenied")
		}
		return testKey, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.Init(ctx, keys, dsn); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if calls.Load() != 3 {
		t.Errorf("key fetched %d times, want 3", calls.Load())
	}
	if get("/healthz") != 200 || get("/readyz") != 200 || get("/v1/models") != 401 {
		t.Fatal("after Init: want healthz 200, readyz 200, unauthenticated proxy 401")
	}
}

func TestInitGivesUpWhenContextEnds(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:1")
	s := New(Config{Upstream: u, Logger: slog.New(slog.DiscardHandler), BackoffBase: time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := s.Init(ctx, secrets.Static(testKey), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	if err == nil || s.Ready() {
		t.Fatalf("want error and not ready, got %v ready=%v", err, s.Ready())
	}
}

type keyFunc func(context.Context) (string, error)

func (f keyFunc) FetchKey(ctx context.Context) (string, error) { return f(ctx) }
