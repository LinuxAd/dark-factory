package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LinuxAd/dark-factory/orchestrator/internal/pricing"
	"github.com/LinuxAd/dark-factory/orchestrator/internal/store"
	"github.com/LinuxAd/dark-factory/orchestrator/internal/usage"
)

// maxJSONBody bounds how much of a non-streaming body is kept for parsing.
// The client still receives every byte beyond it; only metering stops.
const maxJSONBody = 32 << 20

// rowWriteTimeout bounds the agent_calls insert, which runs detached from
// the client's context so cancelled requests are still recorded.
const rowWriteTimeout = 10 * time.Second

var zeroUsage usage.Usage

func (s *Server) modifyResponse(resp *http.Response) error {
	ri := info(resp.Request)
	if ri == nil || !ri.metered {
		return nil
	}
	mb := &meteredBody{rc: resp.Body, s: s, ri: ri, ctx: resp.Request.Context(), status: resp.StatusCode}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		mb.stream = usage.NewStreamParser()
	}
	resp.Body = mb
	return nil
}

// meteredBody tees the upstream body into the usage parser and writes the
// agent_calls row when ReverseProxy closes it.
type meteredBody struct {
	rc     io.ReadCloser
	s      *Server
	ri     *reqInfo
	ctx    context.Context
	status int

	stream   *usage.StreamParser
	buf      bytes.Buffer
	overflow bool

	eof     bool
	readErr error
	once    sync.Once
}

func (b *meteredBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		if b.stream != nil {
			_, _ = b.stream.Write(p[:n])
		} else if !b.overflow {
			if b.buf.Len()+n > maxJSONBody {
				b.overflow = true
				b.buf = bytes.Buffer{}
			} else {
				b.buf.Write(p[:n])
			}
		}
	}
	switch {
	case errors.Is(err, io.EOF):
		b.eof = true
	case err != nil:
		b.readErr = err
	}
	return n, err
}

func (b *meteredBody) Close() error {
	err := b.rc.Close()
	b.once.Do(b.finish)
	return err
}

func (b *meteredBody) finish() {
	var u usage.Usage
	if b.stream != nil {
		u = b.stream.Usage()
	} else if b.overflow {
		b.s.log.Warn("response body too large to meter", "limit_bytes", maxJSONBody)
	} else if b.buf.Len() > 0 {
		var err error
		if u, err = usage.ParseJSON(b.buf.Bytes()); err != nil {
			b.s.log.Warn("response body not parseable for usage", "error", err.Error())
		}
	}
	status := b.status
	if !b.eof {
		// Body ended early. An upstream read error with the client still
		// connected is a gateway problem; anything else means the client
		// went away (its context is cancelled, or writing to it failed).
		status = statusClientClosed
		if b.readErr != nil && b.ctx.Err() == nil {
			status = http.StatusBadGateway
		}
	}
	b.s.record(b.ctx, b.ri, u, status)
}

// record writes one agent_calls row. Responses that carried no usage and no
// model (errors before generation) are not billed, so they cost 0.
func (s *Server) record(ctx context.Context, ri *reqInfo, u usage.Usage, status int) {
	ri.model = u.Model
	var costPtr *string
	if c, ok := pricing.Compute(u); ok {
		v := c.String()
		costPtr = &v
	} else if !u.Found && u.Model == "" {
		v := pricing.Cost(0).String()
		costPtr = &v
	} else {
		s.metrics.unpriced.Inc()
		s.log.Warn("model has no price; cost recorded as NULL", "model", u.Model)
	}
	if u.ErrorType != "" {
		s.log.Warn("upstream reported an error", "error_type", u.ErrorType, "model", u.Model)
	}
	row := store.AgentCall{
		RunID:              ri.caller.RunID,
		TaskID:             ri.caller.TaskID,
		Stage:              ri.caller.Stage,
		Model:              u.Model,
		InputTokens:        u.InputTokens,
		OutputTokens:       u.OutputTokens,
		CacheReadTokens:    u.CacheReadTokens,
		CacheWriteTokens:   u.CacheWriteTokens,
		ServerToolRequests: u.ServerToolCalls,
		CostUSD:            costPtr,
		LatencyMS:          time.Since(ri.start).Milliseconds(),
		HTTPStatus:         status,
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rowWriteTimeout)
	defer cancel()
	if _, err := s.store.InsertAgentCall(wctx, row); err != nil {
		s.log.Error("agent_calls write failed", "run_id", row.RunID, "error", err.Error())
	}
}
