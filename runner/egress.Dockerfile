# syntax=docker/dockerfile:1

# Smokescreen egress proxy (ADR 0001, Decision C1). Upstream ships no
# Dockerfile at this tag, so it is built here from a pinned commit.
# Build context: runner/ (the context is unused; the source is cloned).
# golang:1.26.9 (multi-arch index digest)
FROM --platform=$BUILDPLATFORM golang:1.26.9@sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6 AS build
ARG TARGETOS
ARG TARGETARCH
# v0.1.0
ARG SMOKESCREEN_COMMIT=609eb8931420453daf5893509be0b25b21bd9edb
WORKDIR /src
RUN git init -q . \
    && git remote add origin https://github.com/stripe/smokescreen.git \
    && git fetch -q --depth 1 origin "${SMOKESCREEN_COMMIT}" \
    && git checkout -q FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = "${SMOKESCREEN_COMMIT}"
# Dependencies are vendored upstream, so the build needs no module download.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOFLAGS=-mod=vendor \
    go build -trimpath -ldflags "-s -w" -o /out/smokescreen .

# gcr.io/distroless/static-debian13:nonroot (multi-arch index digest)
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/smokescreen /smokescreen
USER 65532:65532
EXPOSE 4750 9810
ENTRYPOINT ["/smokescreen"]
