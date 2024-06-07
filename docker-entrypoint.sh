#!/usr/bin/env bash
set -Eeo pipefail

OPTS="-addr 0.0.0.0 -certificate /etc/ssl/server.crt -certkey /etc/ssl/server.key"

if [ -n "$ZIPREPORT_API_PORT" ]; then
		OPTS="$OPTS -port $ZIPREPORT_API_PORT"
fi
if [ -n "$ZIPREPORT_API_KEY" ]; then
		OPTS="$OPTS -apikey $ZIPREPORT_API_KEY"
fi
if [ -n "$ZIPREPORT_BASE_PORT" ]; then
		OPTS="$OPTS -baseport $ZIPREPORT_BASE_PORT"
fi
if [ -n "$ZIPREPORT_CONCURRENCY" ]; then
		OPTS="$OPTS -concurrency $ZIPREPORT_CONCURRENCY"
fi
if [ "$ZIPREPORT_DEBUG" == "true" ]; then
		OPTS="$OPTS -debug"
fi
if [ -n "$ZIPREPORT_LOGLEVEL" ]; then
		OPTS="$OPTS -loglevel $ZIPREPORT_LOGLEVEL"
fi
if [ "$ZIPREPORT_CONSOLE" == "true" ]; then
		OPTS="$OPTS -console"
fi

exec zipreport-server $OPTS