# syntax=docker/dockerfile:1

# ── Build: static binary, no cgo (ncruces/go-sqlite3 runs SQLite as Wasm) ──
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=
# -tags release leaves out the demo mode (O10).
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags release -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" -o /out/jidoka ./cmd/jidoka \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/jidoka-shim ./cmd/jidoka-shim

# ── Runtime: the tools jobs may use (J11) ──
#
#   uid 10001   jidoka   server: /data, database, MASTER_KEY
#   uid 20000+  per job  scripts: only their own state, home and $TMPDIR (Z3)
#
# The server gives every job its own uid and gid, counted up from
# JIDOKA_UID_BASE (no /etc/passwd entry needed), and switches to it per
# run; the binary carries CAP_SETUID/SETGID (switch user), CAP_KILL (end
# the job's process group) and CAP_CHOWN (hand folders to the job's uid).
# tini reaps orphans of detached children (J4).
FROM alpine:3.24
RUN apk add --no-cache tini su-exec libcap bash curl jq python3 uv tzdata ca-certificates \
 && addgroup -S -g 10001 jidoka && adduser -S -u 10001 -G jidoka -h /app jidoka \
 && mkdir -p /data /jobs && chown jidoka:jidoka /data
ENV DATA_DIR=/data JOBS_DIR=/jobs JIDOKA_SHIM=/usr/local/bin/jidoka-shim JIDOKA_UID_BASE=20000 UV_PYTHON_DOWNLOADS=never
COPY --from=build /out/jidoka /out/jidoka-shim /usr/local/bin/
# Only uid 10001 may run the capable server binary; jobs get the shim.
RUN chown root:jidoka /usr/local/bin/jidoka && chmod 750 /usr/local/bin/jidoka \
 && setcap cap_setuid,cap_setgid,cap_kill,cap_chown+ep /usr/local/bin/jidoka
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["jidoka", "healthcheck"]
ENTRYPOINT ["tini", "-g", "--", "entrypoint.sh"]
CMD ["serve"]
