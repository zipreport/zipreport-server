FROM golang as go

ARG goproxy="https://proxy.golang.org,direct"
LABEL org.opencontainers.image.source https://github.com/zipreport/zipreport-server


COPY . /app
WORKDIR /app
RUN go env -w GOPROXY=$goproxy
RUN go mod download
RUN go build -o zipreport-server ./cmd/zipreport-server/main.go
RUN go build -o browser-update ./cmd/browser-update/main.go
RUN ./browser-update
RUN mkdir -p /app/config/ssl


FROM ubuntu:jammy

COPY --from=go /root/.cache/rod /root/.cache/rod
RUN ln -s /root/.cache/rod/browser/$(ls /root/.cache/rod/browser)/chrome /usr/bin/chrome

RUN touch /.dockerenv

RUN mkdir -p /app/config/ssl
WORKDIR /app

COPY --from=go /app/zipreport-server /app/
COPY --from=go /app/browser-update /app/
COPY --from=go /app/docker-entrypoint.sh /app/
COPY --from=go /app/config/config.sample.json /app/config/config.json

RUN chmod +x /app/docker-entrypoint.sh /app/zipreport-server /app/browser-update
ARG apt_sources="http://archive.ubuntu.com"

RUN sed -i "s|http://archive.ubuntu.com|$apt_sources|g" /etc/apt/sources.list && \
    apt-get update > /dev/null && \
    apt-get install --no-install-recommends -y \
    # chromium dependencies
    libnss3 \
    libxss1 \
    libasound2 \
    libxtst6 \
    libgtk-3-0 \
    libgbm1 \
    ca-certificates \
    # fonts
    fonts-liberation fonts-noto-color-emoji fonts-noto-cjk \
    # timezone
    tzdata \
    # process reaper
    dumb-init \
    # headful mode support, for example: $ xvfb-run chromium-browser --remote-debugging-port=9222
    xvfb \
    > /dev/null && \
    # cleanup
    rm -rf /var/lib/apt/lists/*

STOPSIGNAL SIGINT
EXPOSE 6543/tcp

ENTRYPOINT ["/app/docker-entrypoint.sh"]