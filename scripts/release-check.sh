#!/bin/sh
# Fails when a release binary carries the demo mode or Studio Weber, the
# demo world (names of the studio, its people, customers and vendors),
# or still has a demo command. Usage: scripts/release-check.sh <binary>
# Adapted from Andon's scripts/release-check.sh (468474a).
set -eu
bin="$1"
world="$(dirname "$0")/../internal/demo/world.json"

# Fixed markers plus every identifying name from the world.
markers="jidoka/internal/demo
DEMO_TODAY
data-demo
.jidoka-demo
Studio Weber
studio-weber"
if [ -r "$world" ]; then
	markers="$markers
$(python3 - "$world" <<'PY'
import json, sys
w = json.load(open(sys.argv[1], encoding="utf-8"))
names = {w["studio"]["name"]}
names |= {p["name"] for p in w.get("people", [])}
names |= {c["name"] for c in w.get("customers", [])}
names |= {v["name"] for v in w.get("vendors", [])}
print("\n".join(sorted(n for n in names if len(n) >= 8)))
PY
)"
fi

found=0
printf '%s\n' "$markers" | while IFS= read -r m; do
	[ -n "$m" ] || continue
	if grep -aqF -- "$m" "$bin"; then
		echo "release-check: \"$m\" found in $bin" >&2
		exit 1
	fi
done || found=1
if [ "$found" -ne 0 ]; then
	exit 1
fi

# The binary must not know the command either.
if out="$("$bin" demo 2>&1)"; then
	echo "release-check: $bin runs a demo command" >&2
	exit 1
fi
case "$out" in
*"unknown command"*) ;;
*)
	echo "release-check: unexpected answer to the demo command: $out" >&2
	exit 1
	;;
esac

echo "release-check: no demo data in $bin"
