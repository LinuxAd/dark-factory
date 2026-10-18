// Phase 0 smoke test (plan Step 6, AC6): one Agent SDK query through the
// gateway. The pod holds only the per-Job gateway token in
// ANTHROPIC_AUTH_TOKEN and ANTHROPIC_BASE_URL points at the gateway; the SDK
// reads both from the environment. Exits non-zero unless the reply is OK.
import { query } from "@anthropic-ai/claude-agent-sdk";

for (const name of ["ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"]) {
  if (!process.env[name]) {
    console.error(`smoke: ${name} is not set`);
    process.exit(2);
  }
}

let result;
try {
  for await (const message of query({
    prompt: "Reply with exactly OK and nothing else.",
    options: {
      maxTurns: 1,
      tools: [],
      settingSources: [],
      cwd: process.env.HOME,
    },
  })) {
    if (message.type === "result") {
      result = message;
    }
  }
} catch (err) {
  console.error(`smoke: query failed: ${err?.message ?? err}`);
  process.exit(1);
}

if (!result) {
  console.error("smoke: no result message");
  process.exit(1);
}

const text = typeof result.result === "string" ? result.result.trim() : "";
console.log(
  JSON.stringify({
    subtype: result.subtype,
    is_error: result.is_error,
    result: text,
    num_turns: result.num_turns,
    duration_ms: result.duration_ms,
    total_cost_usd: result.total_cost_usd,
    usage: result.usage,
  }),
);

if (result.subtype !== "success" || result.is_error || !/^OK\.?$/i.test(text)) {
  console.error("smoke: FAIL (expected the reply OK)");
  process.exit(1);
}
console.log("smoke: OK");
