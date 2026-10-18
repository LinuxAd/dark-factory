package usage

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func parseStream(t *testing.T, b []byte, chunk int) Usage {
	t.Helper()
	p := NewStreamParser()
	for len(b) > 0 {
		n := min(chunk, len(b))
		if _, err := p.Write(b[:n]); err != nil {
			t.Fatalf("write: %v", err)
		}
		b = b[n:]
	}
	return p.Usage()
}

func TestParseJSON(t *testing.T) {
	got, err := ParseJSON(fixture(t, "message.json"))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	want := Usage{
		Model:              "claude-sonnet-5-5",
		InputTokens:        1520,
		OutputTokens:       42,
		CacheReadTokens:    10240,
		CacheWriteTokens:   2048,
		CacheWrite5mTokens: 2048,
		CacheWrite1hTokens: 0,
		Found:              true,
	}
	assertUsage(t, got, want)
}

func TestParseJSONErrorBody(t *testing.T) {
	got, err := ParseJSON([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	assertUsage(t, got, Usage{ErrorType: "invalid_request_error"})
}

func TestParseJSONInvalid(t *testing.T) {
	if _, err := ParseJSON([]byte("not json")); err == nil {
		t.Fatal("want error for invalid JSON")
	}
}

func TestStreamLastValueMessageDelta(t *testing.T) {
	// Input and cache tokens come from message_start; output tokens are the
	// last message_delta value (87), never the sum (92) or message_start's 1.
	want := Usage{
		Model:              "claude-opus-5-5",
		InputTokens:        812,
		OutputTokens:       87,
		CacheReadTokens:    4096,
		CacheWriteTokens:   1024,
		CacheWrite5mTokens: 1024,
		Found:              true,
	}
	assertUsage(t, parseStream(t, fixture(t, "stream.sse"), 1<<20), want)
}

func TestStreamSplitFrames(t *testing.T) {
	b := fixture(t, "stream.sse")
	whole := parseStream(t, b, len(b))
	for _, chunk := range []int{1, 2, 3, 7, 64, 100} {
		got := parseStream(t, b, chunk)
		if got != whole {
			t.Errorf("chunk %d: got %+v, want %+v", chunk, got, whole)
		}
	}
}

func TestStreamCRLF(t *testing.T) {
	b := bytes.ReplaceAll(fixture(t, "stream.sse"), []byte("\n"), []byte("\r\n"))
	if got := parseStream(t, b, 5); got.OutputTokens != 87 || got.InputTokens != 812 {
		t.Errorf("CRLF stream: got %+v", got)
	}
}

func TestStreamServerToolUsage(t *testing.T) {
	got := parseStream(t, fixture(t, "stream_server_tool.sse"), 13)
	want := Usage{
		Model:             "claude-opus-5-5",
		InputTokens:       9850, // non-null message_delta value overwrites message_start
		OutputTokens:      215,
		CacheReadTokens:   512,
		CacheWriteTokens:  0, // null in message_delta keeps message_start's value
		WebSearchRequests: 2,
		WebFetchRequests:  1,
		ServerToolCalls:   3,
		Found:             true,
	}
	assertUsage(t, got, want)
}

func TestStreamErrorEvent(t *testing.T) {
	got := parseStream(t, fixture(t, "stream_error.sse"), 9)
	want := Usage{
		Model:        "claude-haiku-5-5",
		InputTokens:  300,
		OutputTokens: 1,
		ErrorType:    "overloaded_error",
		Found:        true,
	}
	assertUsage(t, got, want)
}

func TestStreamIgnoresPingAndUnknownEvents(t *testing.T) {
	s := "event: ping\ndata: {\"type\": \"ping\"}\n\n" +
		"event: future_event\ndata: {\"type\":\"future_event\",\"usage\":{\"output_tokens\":999}}\n\n" +
		": comment line\n\n"
	if got := parseStream(t, []byte(s), 4); got != (Usage{}) {
		t.Errorf("got %+v, want zero usage", got)
	}
}

func TestStreamMultiLineData(t *testing.T) {
	s := "event: message_delta\ndata: {\"type\":\"message_delta\",\ndata: \"usage\":{\"output_tokens\":12}}\n\n"
	if got := parseStream(t, []byte(s), 3); got.OutputTokens != 12 {
		t.Errorf("got %+v, want output 12", got)
	}
}

func TestStreamOversizedLineIsSkipped(t *testing.T) {
	big := "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"" + strings.Repeat("x", maxLineBytes+10) + "\"}}\n\n"
	s := big + "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\n"
	if got := parseStream(t, []byte(s), 4096); got.OutputTokens != 4 {
		t.Errorf("got %+v, want output 4 after oversized line", got)
	}
}

func TestStreamIncompleteFinalEventDiscarded(t *testing.T) {
	s := "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}"
	if got := parseStream(t, []byte(s), 8); got.OutputTokens != 4 {
		t.Errorf("got %+v, want output 4 (unterminated event dropped)", got)
	}
}

func assertUsage(t *testing.T, got, want Usage) {
	t.Helper()
	if got != want {
		t.Errorf("usage mismatch\n got: %+v\nwant: %+v", got, want)
	}
}
