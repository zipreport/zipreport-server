FROM golang as go

ARG goproxy="https://proxy.golang.org,direct"
LABEL org.opencontainers.image.source https://github.com/zipreport/zipreport-server


COPY . /app
WORKDIR /app
RUN go env -w GOPROXY=$goproxy
RUN go mod download
RUN go build ./cmd/zipreport-server
RUN go run ./cmd/browser-update

# generate default self-signed cert
RUN openssl req -x509 -nodes -newkey rsa:4096 -keyout /etc/ssl/server.key -out /etc/ssl/server.crt -days 3650 \
    	      -subj "/C=PT/ST=Lisbon/L=Lisbon/O=ZipReport/OU=RD/CN=zipreport-server.local"

FROM ubuntu:jammy

COPY --from=go /root/.cache/rod /root/.cache/rod
RUN ln -s /root/.cache/rod/browser/$(ls /root/.cache/rod/browser)/chrome /usr/bin/chrome

RUN touch /.dockerenv

COPY --from=go /app/zipreport-server /usr/bin/
COPY --from=go /app/docker-entrypoint.sh /usr/bin/
COPY --from=go /etc/ssl/server.crt /etc/ssl/
COPY --from=go /etc/ssl/server.key /etc/ssl/

RUN chmod +x /usr/bin/docker-entrypoint.sh
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

ENTRYPOINT ["docker-entrypoint.sh"]