FROM --platform=$BUILDPLATFORM golang:1.26.3 AS builder

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

FROM cgr.dev/chainguard/wolfi-base

LABEL org.opencontainers.image.source=https://github.com/zipreport/zipreport-server

# Wolfi is glibc-based, so the glibc Chromium build downloaded by browser-update runs unmodified.
# shadow provides useradd; the rest are Chromium's runtime libraries. libudev is required or
# Chrome aborts at startup (udev_loader.cc); libnss is Mozilla NSS (Wolfi's "nss" is glibc NSS).
RUN apk add --no-cache \
    shadow \
    libnss \
    libudev \
    libxscrnsaver \
    libxtst \
    libxcomposite \
    libxdamage \
    libxrandr \
    libxkbcommon \
    libx11 \
    libdrm \
    mesa-gbm \
    gtk-3 \
    pango \
    cups-libs \
    at-spi2-core \
    dbus-libs \
    alsa-lib \
    ca-certificates-bundle \
    font-liberation \
    font-noto-emoji \
    font-noto-cjk \
    tzdata \
    dumb-init \
    xvfb-run

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
