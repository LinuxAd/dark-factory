// Package usage extracts token usage from Anthropic Messages API responses,
// both non-streaming JSON bodies and server-sent event streams.
//
// Streaming rule (ADR 0001, Decision D): input and cache tokens come from
// message_start.message.usage; then every non-null field of each later
// message_delta.usage overwrites the stored value. Those fields are
// cumulative, so they are never summed.
package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// maxLineBytes bounds the memory one SSE line can take. Lines longer than
// this (large content blocks) are skipped; usage never lives in them.
const maxLineBytes = 8 << 20

// Usage is what one response reports. The struct is comparable so tests can
// use ==.
type Usage struct {
	Model              string
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64 // cache_creation_input_tokens, all TTLs
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	WebSearchRequests  int64
	WebFetchRequests   int64
	ServerToolCalls    int64 // sum of every server_tool_use counter
	ErrorType          string
	Found              bool // a usage object was seen
}

type rawCacheCreation struct {
	Ephemeral5m *int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h *int64 `json:"ephemeral_1h_input_tokens"`
}

type rawUsage struct {
	InputTokens              *int64                     `json:"input_tokens"`
	OutputTokens             *int64                     `json:"output_tokens"`
	CacheCreationInputTokens *int64                     `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64                     `json:"cache_read_input_tokens"`
	CacheCreation            *rawCacheCreation          `json:"cache_creation"`
	ServerToolUse            map[string]json.RawMessage `json:"server_tool_use"`
}

type rawError struct {
	Type string `json:"type"`
}

// rawEvent covers the shapes we read: a full message body, message_start,
// message_delta and error. Unused fields are ignored by encoding/json.
type rawEvent struct {
	Type    string    `json:"type"`
	Model   string    `json:"model"`
	Usage   *rawUsage `json:"usage"`
	Error   *rawError `json:"error"`
	Message *struct {
		Model string    `json:"model"`
		Usage *rawUsage `json:"usage"`
	} `json:"message"`
}

// state accumulates usage with the overwrite-non-null rule.
type state struct {
	u          Usage
	serverTool map[string]int64
}

func (s *state) apply(r *rawUsage) {
	if r == nil {
		return
	}
	s.u.Found = true
	set := func(dst *int64, v *int64) {
		if v != nil {
			*dst = *v
		}
	}
	set(&s.u.InputTokens, r.InputTokens)
	set(&s.u.OutputTokens, r.OutputTokens)
	set(&s.u.CacheWriteTokens, r.CacheCreationInputTokens)
	set(&s.u.CacheReadTokens, r.CacheReadInputTokens)
	if r.CacheCreation != nil {
		set(&s.u.CacheWrite5mTokens, r.CacheCreation.Ephemeral5m)
		set(&s.u.CacheWrite1hTokens, r.CacheCreation.Ephemeral1h)
	}
	for k, raw := range r.ServerToolUse {
		var n int64
		if json.Unmarshal(raw, &n) != nil {
			continue // null or a non-counter field
		}
		if s.serverTool == nil {
			s.serverTool = map[string]int64{}
		}
		s.serverTool[k] = n
	}
}

func (s *state) result() Usage {
	u := s.u
	u.WebSearchRequests = s.serverTool["web_search_requests"]
	u.WebFetchRequests = s.serverTool["web_fetch_requests"]
	u.ServerToolCalls = 0
	for _, n := range s.serverTool {
		u.ServerToolCalls += n
	}
	return u
}

// ParseJSON reads a non-streaming response body. An API error body yields a
// zero Usage with ErrorType set.
func ParseJSON(body []byte) (Usage, error) {
	var ev rawEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return Usage{}, fmt.Errorf("decode response body: %w", err)
	}
	var s state
	s.u.Model = ev.Model
	if ev.Error != nil {
		s.u.ErrorType = ev.Error.Type
	}
	s.apply(ev.Usage)
	return s.result(), nil
}

// StreamParser is an io.Writer that consumes SSE bytes in arbitrary chunks.
type StreamParser struct {
	line     []byte
	skipping bool // current line exceeded maxLineBytes
	data     []byte
	hasData  bool
	s        state
}

// NewStreamParser returns an empty parser.
func NewStreamParser() *StreamParser { return &StreamParser{} }

// Write never fails; malformed events are ignored so the proxied stream is
// never affected by parsing.
func (p *StreamParser) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			p.appendLine(b)
			break
		}
		p.appendLine(b[:i])
		if !p.skipping {
			p.handleLine(bytes.TrimSuffix(p.line, []byte("\r")))
		}
		p.line = p.line[:0]
		p.skipping = false
		b = b[i+1:]
	}
	return n, nil
}

func (p *StreamParser) appendLine(b []byte) {
	if p.skipping {
		return
	}
	if len(p.line)+len(b) > maxLineBytes {
		p.skipping = true
		p.line = p.line[:0]
		return
	}
	p.line = append(p.line, b...)
}

func (p *StreamParser) handleLine(line []byte) {
	if len(line) == 0 {
		p.dispatch()
		return
	}
	v, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return // event:, id:, retry: and comments carry no usage
	}
	v = bytes.TrimPrefix(v, []byte(" "))
	if p.hasData {
		p.data = append(p.data, '\n')
	}
	p.data = append(p.data, v...)
	p.hasData = true
}

var interesting = [][]byte{[]byte(`"message_start"`), []byte(`"message_delta"`), []byte(`"error"`)}

func (p *StreamParser) dispatch() {
	defer func() { p.data, p.hasData = p.data[:0], false }()
	if !p.hasData {
		return
	}
	// Cheap filter so large content blocks are not decoded.
	found := false
	for _, m := range interesting {
		if bytes.Contains(p.data, m) {
			found = true
			break
		}
	}
	if !found {
		return
	}
	var ev rawEvent
	if json.Unmarshal(p.data, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			p.s.u.Model = ev.Message.Model
			p.s.apply(ev.Message.Usage)
		}
	case "message_delta":
		p.s.apply(ev.Usage)
	case "error":
		if ev.Error != nil {
			p.s.u.ErrorType = ev.Error.Type
		}
	}
}

// Usage returns the usage seen so far. An unterminated final event is not
// counted, as the SSE format requires.
func (p *StreamParser) Usage() Usage { return p.s.result() }
