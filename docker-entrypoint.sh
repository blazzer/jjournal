#!/bin/sh
set -e
mkdir -p /data/userpics /data/images
chown -R journal:journal /data
exec su-exec journal:journal /usr/local/bin/journal
