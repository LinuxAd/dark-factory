# Usage fixtures

**These fixtures are synthetic.** They were written by hand from the
documented Anthropic Messages API format (non-streaming JSON and the SSE event
sequence `message_start`, `content_block_*`, `ping`, `message_delta`,
`message_stop`, `error`), because no API key was available when the gateway
was built. The token numbers are arbitrary but chosen so tests can tell the
fields apart.

| File | What it models |
|---|---|
| `message.json` | Non-streaming `POST /v1/messages` response with cache read and 5-minute cache write usage |
| `stream.sse` | Streaming response; two `message_delta` events so the last-value rule is exercised (output 5 then 87, never 92) |
| `stream_server_tool.sse` | Streaming response with a `web_search` server tool; the final `message_delta` carries cumulative input/cache tokens (one field `null`) and `server_tool_use` counts |
| `stream_error.sse` | Stream that ends with an `error` event (`overloaded_error`) after `message_start` |
| `count_tokens.json` | `POST /v1/messages/count_tokens` response (passes through, writes no row) |

## Replacing them with real captures (Step 0b)

Adam captures real responses from his own shell. The key is read from the
environment and never written to disk; review each file before committing to
confirm it holds no key or personal content.

```sh
cd orchestrator/internal/usage/testdata

# Non-streaming
curl -sS https://api.anthropic.com/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"claude-sonnet-5-5","max_tokens":64,"messages":[{"role":"user","content":"Reply with OK"}]}' \
  > message.json

# Streaming (-N disables curl buffering so the bytes are exactly what was sent)
curl -sSN https://api.anthropic.com/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"claude-opus-5-5","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"Say hello"}]}' \
  > stream.sse

# Streaming with a server tool
curl -sSN https://api.anthropic.com/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"claude-opus-5-5","max_tokens":1024,"stream":true,"tools":[{"type":"web_search_20260209","name":"web_search","max_uses":2}],"messages":[{"role":"user","content":"When is the next Go release? Search the web."}]}' \
  > stream_server_tool.sse
```

The exact token values asserted in `usage_test.go` and the gateway tests come
from these files, so update those expectations in the same commit when a
fixture is replaced (the spec they encode changed: real data replaced
synthetic). `stream_error.sse` cannot be captured on demand; keep it synthetic.
