FROM --platform=$BUILDPLATFORM golang:1.24.12 AS builder

ARG TARGETARCH
ARG TARGETOS=linux
ARG goproxy="https://proxy.golang.org,direct"
ARG VERSION="dev"

WORKDIR /app

# Cache dependency downloads — only re-runs if go.mod/go.sum change
COPY go.mod go.sum ./
RUN go env -w GOPROXY=$goproxy && go mod download

# Cross-compile binaries with stripped debug info and injected version
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w -X main.Version=${VERSION}" -o zipreport-server ./cmd/zipreport-server/main.go && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o browser-update ./cmd/browser-update/main.go && \
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

WORKDIR /app

COPY --from=builder /app/zipreport-server /app/
COPY --from=builder /app/browser-update /app/
COPY --from=builder /app/config/config.sample.json /app/config/config.json

# Non-root user (create before browser download so we can use the correct home dir)
RUN useradd -r -m -s /bin/false zipreport && \
    mkdir -p /app/config/ssl

# Download browser for the target platform (supports amd64 and arm64)
ENV HOME=/home/zipreport
RUN /app/browser-update
RUN ln -s "/home/zipreport/.cache/rod/browser/$(ls -1 /home/zipreport/.cache/rod/browser | head -1)/chrome" /usr/bin/chrome
RUN chown -R zipreport:zipreport /home/zipreport/.cache /app

RUN touch /.dockerenv

USER zipreport
STOPSIGNAL SIGINT
EXPOSE 6543/tcp

ENTRYPOINT ["dumb-init", "--", "/app/zipreport-server", "-c", "config/config.json"]
