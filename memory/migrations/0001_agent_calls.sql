-- +goose Up
-- One row per upstream model request made through the gateway (SPEC.md §6.5).
-- run_id has no foreign key until the runs table exists (Phase 1).
CREATE TABLE agent_calls (
    id                   bigserial PRIMARY KEY,
    run_id               text        NOT NULL,
    task_id              text        NULL,
    stage                text        NOT NULL,
    model                text        NOT NULL DEFAULT '',
    input_tokens         bigint      NOT NULL DEFAULT 0,
    output_tokens        bigint      NOT NULL DEFAULT 0,
    cache_read_tokens    bigint      NOT NULL DEFAULT 0,
    cache_write_tokens   bigint      NOT NULL DEFAULT 0,
    server_tool_requests integer     NOT NULL DEFAULT 0,
    cost_usd             numeric(12,6) NULL,
    latency_ms           integer     NOT NULL,
    http_status          integer     NOT NULL,
    transcript_s3_key    text        NULL,
    created_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX agent_calls_run_id_idx ON agent_calls (run_id);
CREATE INDEX agent_calls_created_at_idx ON agent_calls (created_at);

-- +goose Down
DROP TABLE agent_calls;
