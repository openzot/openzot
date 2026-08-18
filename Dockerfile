# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.6

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath \
    -ldflags "-s -w -X github.com/openzot/openzot/internal/version.Version=${VERSION}" \
    -o /out/zot ./cmd/zot

FROM alpine:3.22

# The release image is deliberately lean: Zot, TLS roots, timezone data, and
# Alpine's POSIX shell. Zotui injects its own worker binary into separately
# configured environment images, which provide any development tools.
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 zot && \
    adduser -S -D -H -u 10001 -G zot zot && \
    mkdir -p \
        /home/zot/.cache \
        /home/zot/.config/zot \
        /home/zot/.local/share/zot \
        /home/zot/.run \
        /workspace && \
    chown -R zot:zot /home/zot /workspace

COPY --from=build /out/zot /usr/local/bin/zot
COPY configs/zot.example.yaml \
    /usr/local/share/zot/zot.example.yaml

ENV HOME=/home/zot \
    XDG_CACHE_HOME=/home/zot/.cache \
    XDG_CONFIG_HOME=/home/zot/.config \
    XDG_DATA_HOME=/home/zot/.local/share \
    XDG_RUNTIME_DIR=/home/zot/.run \
    ZOT_CONFIG=/home/zot/.config/zot/config.yaml

USER zot
WORKDIR /workspace

# /workspace is the directory the agent works in - mount your checkout there.
VOLUME ["/workspace", "/home/zot/.config/zot"]

# No HEALTHCHECK: zot is a one-shot CLI (or a stdio ACP server), not a daemon.
ENTRYPOINT ["zot"]
