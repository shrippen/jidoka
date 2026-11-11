#!/bin/sh
# Runs next to the probe in scripts/image-test.sh and keeps a marker in
# its environment and a file in its state folder (Z3).
echo "holder-state" > "$JIDOKA_STATE/private"
exec sleep 77
