#!/bin/sh

openssl req -x509 -nodes -newkey rsa:4096 -keyout ./ssl/server.key -out ./ssl/server.crt -days 3650 \
    	      -subj "/C=PT/ST=Lisbon/L=Lisbon/O=ZipReport/OU=RD/CN=zipreport-server.local"