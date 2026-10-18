# syntax=docker/dockerfile:1

# Smokescreen egress proxy (ADR 0001, Decision C1). Upstream ships no
# Dockerfile at this tag, so it is built here from a pinned commit.
# Build context: runner/ (the context is unused; the source is cloned).
FROM --platform=$BUILDPLATFORM golang:1.26.9 AS build
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

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/smokescreen /smokescreen
USER 65532:65532
EXPOSE 4750 9810
ENTRYPOINT ["/smokescreen"]
