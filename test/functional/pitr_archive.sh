#!/bin/sh
# PostgreSQL supplies the source path and archive name inside the owned fixture.
set -eu
test "$#" -eq 2
case "$2" in ''|*..*|.|*[!A-F0-9a-z.]*) exit 1 ;; esac
destination="/archive/$2"
if test -e "$destination"; then
    # Retrying an identical segment succeeds; different bytes never overwrite it.
    cmp -s -- "$1" "$destination"
else
    temporary="$destination.tmp"
    trap 'rm -f -- "$temporary"' 0
    trap 'exit 1' HUP INT TERM
    cp -- "$1" "$temporary"
    mv -- "$temporary" "$destination"
fi
