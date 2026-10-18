# syntax=docker/dockerfile:1

# Runner image, Phase 0 smoke subset (plan Step 6): Node.js LTS with the
# Claude Agent SDK, Go, and curl. It runs as a non-root user with a read-only
# root filesystem; the Job mounts emptyDirs at /home/runner and /tmp.
# node:24.22.0-trixie-slim (multi-arch index digest)
FROM node:24.22.0-trixie-slim@sha256:7f486cabe892bff8fef04c0f7da8dba84a560be3e4e2740b7a86afc57ebec315

ARG TARGETARCH
# Pinned with the checksums published at https://go.dev/dl/?mode=json.
ARG GO_VERSION=1.27.2
ARG GO_SHA256_AMD64=ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5
ARG GO_SHA256_ARM64=94f3e30b8e374bc285e7dadc11e0865726b9bc6e85b841ccceaabc0214c6b7c8

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) sha="${GO_SHA256_AMD64}" ;; \
      arm64) sha="${GO_SHA256_ARM64}" ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo /tmp/go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz"; \
    echo "${sha}  /tmp/go.tgz" | sha256sum -c -; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm /tmp/go.tgz

# The Agent SDK and its bundled Claude Code binary, from the lockfile. Install
# scripts are off, as they will be for generated projects (SPEC.md 7.6).
WORKDIR /opt/runner/smoke
COPY smoke/package.json smoke/package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund && npm cache clean --force
COPY smoke/smoke.mjs ./
COPY --chmod=0755 probe/probe.sh /opt/runner/probe/probe.sh

RUN groupadd --gid 10001 runner \
    && useradd --uid 10001 --gid 10001 --home-dir /home/runner --create-home --shell /bin/bash runner

ENV HOME=/home/runner \
    PATH=/usr/local/go/bin:/home/runner/go/bin:${PATH} \
    GOPATH=/home/runner/go \
    GOCACHE=/tmp/go-build \
    GOTOOLCHAIN=local \
    NPM_CONFIG_CACHE=/tmp/npm-cache \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    DISABLE_TELEMETRY=1 \
    DISABLE_AUTOUPDATER=1

USER 10001:10001
WORKDIR /home/runner
CMD ["node", "/opt/runner/smoke/smoke.mjs"]
