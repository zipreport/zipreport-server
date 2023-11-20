#!/usr/bin/env bash
set -Eeo pipefail

OPTS="-addr 0.0.0.0 -certificate /etc/ssl/server.crt -certkey /etc/ssl/server.key"

if [ $ZIPREPORT_API_PORT ] ; then
		OPTS="$OPTS -port $ZIPREPORT_API_PORT"
fi
if [ $ZIPREPORT_API_KEY ] ; then
		OPTS="$OPTS -apikey $ZIPREPORT_API_KEY"
fi
if [ $ZIPREPORT_BASE_PORT ] ; then
		OPTS="$OPTS -baseport $ZIPREPORT_BASE_PORT"
fi
if [ $ZIPREPORT_SSL_CERTIFICATE ] ; then
		OPTS="$OPTS -certificate $ZIPREPORT_SSL_CERTIFICATE -certkey $ZIPREPORT_SSL_KEY"
fi
if [ $ZIPREPORT_CONCURRENCY ] ; then
		OPTS="$OPTS -concurrency $ZIPREPORT_CONCURRENCY"
fi
if [ $ZIPREPORT_DEBUG ] ; then
		OPTS="$OPTS -debug $ZIPREPORT_DEBUG"
fi
if [ $ZIPREPORT_LOGLEVEL ] ; then
		OPTS="$OPTS -loglevel $ZIPREPORT_LOGLEVEL"
fi

exec zipreport-server $OPTS