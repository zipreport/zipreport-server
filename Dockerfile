FROM golang:1.24.12 AS builder

ARG goproxy="https://proxy.golang.org,direct"
ARG VERSION="dev"

WORKDIR /app

# Cache dependency downloads — only re-runs if go.mod/go.sum change
COPY go.mod go.sum ./
RUN go env -w GOPROXY=$goproxy && go mod download

# Build binaries with stripped debug info and injected version
COPY . .
RUN go build -ldflags="-s -w -X main.Version=${VERSION}" -o zipreport-server ./cmd/zipreport-server/main.go && \
    go build -ldflags="-s -w" -o browser-update ./cmd/browser-update/main.go && \
    ./browser-update && \
    mkdir -p /app/config/ssl

FROM ubuntu:jammy

LABEL org.opencontainers.image.source=https://github.com/zipreport/zipreport-server

ARG apt_sources="http://archive.ubuntu.com"

RUN sed -i "s|http://archive.ubuntu.com|$apt_sources|g" /etc/apt/sources.list && \
    apt-get update > /dev/null && \
    apt-get install --no-install-recommends -y \
    libnss3 libxss1 libasound2 libxtst6 libgtk-3-0 libgbm1 \
    ca-certificates \
    fonts-liberation fonts-noto-color-emoji fonts-noto-cjk \
    tzdata \
    dumb-init \
    xvfb \
    > /dev/null && \
    rm -rf /var/lib/apt/lists/*

# Browser cache
COPY --from=builder /root/.cache/rod /root/.cache/rod
RUN ln -s "/root/.cache/rod/browser/$(ls -1 /root/.cache/rod/browser | head -1)/chrome" /usr/bin/chrome

# Non-root user
RUN useradd -r -s /bin/false zipreport && \
    mkdir -p /app/config/ssl && \
    chown -R zipreport:zipreport /app /root/.cache/rod

WORKDIR /app

COPY --from=builder --chown=zipreport:zipreport /app/zipreport-server /app/
COPY --from=builder --chown=zipreport:zipreport /app/browser-update /app/
COPY --from=builder --chown=zipreport:zipreport /app/config/config.sample.json /app/config/config.json

RUN touch /.dockerenv

USER zipreport
STOPSIGNAL SIGINT
EXPOSE 6543/tcp

ENTRYPOINT ["dumb-init", "--", "/app/zipreport-server", "-c", "config/config.json"]
