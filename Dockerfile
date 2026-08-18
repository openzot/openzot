# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.6

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN set -eu; \
    mkdir -p /out /tmp/zotui-workers; \
    for arch in amd64 arm64; do \
        CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
            -ldflags "-s -w -X github.com/openzot/openzot/internal/version.Version=${VERSION}" \
            -o "/tmp/zotui-workers/zot-linux-$arch" ./cmd/zot; \
        gzip -n -9 -c "/tmp/zotui-workers/zot-linux-$arch" \
            > "internal/zotui/worker/artifacts/zot-linux-$arch.gz"; \
    done; \
    for cmd in zot zotui; do \
        CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath \
            -ldflags "-s -w -X github.com/openzot/openzot/internal/version.Version=${VERSION}" \
            -o "/out/$cmd" "./cmd/$cmd"; \
    done

FROM alpine:3.22

# The release image is deliberately lean: Zot, Zotui, TLS roots, timezone data,
# and Alpine's POSIX shell. Zotui injects its embedded worker binary into
# separately configured environment images, which provide development tools.
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 zot && \
    adduser -S -D -H -u 10001 -G zot zot && \
    mkdir -p \
        /home/zot/.cache \
        /home/zot/.config/zot \
        /home/zot/.config/zotui \
        /home/zot/.local/share/zot \
        /home/zot/.local/state/zotui \
        /home/zot/.run \
        /workspace && \
    chown -R zot:zot /home/zot /workspace

COPY --from=build /out/zot /usr/local/bin/zot
COPY --from=build /out/zotui /usr/local/bin/zotui
COPY configs/zot.example.yaml \
    /usr/local/share/zot/zot.example.yaml
COPY configs/zotui.example.yaml \
    /usr/local/share/zot/zotui.example.yaml

ENV HOME=/home/zot \
    XDG_CACHE_HOME=/home/zot/.cache \
    XDG_CONFIG_HOME=/home/zot/.config \
    XDG_DATA_HOME=/home/zot/.local/share \
    XDG_RUNTIME_DIR=/home/zot/.run \
    ZOT_CONFIG=/home/zot/.config/zot/config.yaml \
    ZOTUI_ADDR=0.0.0.0:8080

USER zot
WORKDIR /workspace

# /workspace is the directory the agent works in - mount your checkout there.
VOLUME ["/workspace", "/home/zot/.config/zot"]

EXPOSE 8080

# No HEALTHCHECK: zot is a one-shot CLI (or a stdio ACP server), not a daemon.
ENTRYPOINT ["zot"]
