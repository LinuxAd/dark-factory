-- +goose Up
-- Per-Job bearer tokens. Only the SHA-256 of a token is stored; the token
-- itself is printed once by `gateway mint-token` and never persisted.
CREATE TABLE gateway_tokens (
    token_sha256 bytea       PRIMARY KEY,
    run_id       text        NOT NULL,
    task_id      text        NULL,
    stage        text        NOT NULL,
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT gateway_tokens_sha256_len CHECK (octet_length(token_sha256) = 32)
);

-- +goose Down
DROP TABLE gateway_tokens;
