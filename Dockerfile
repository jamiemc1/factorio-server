# --- Build the Go supervisor -------------------------------------------------
FROM golang:1.26 AS supervisor-build
WORKDIR /src
# Prime the module cache first for better layer caching.
COPY supervisor/go.mod supervisor/go.sum ./
RUN go mod download
COPY supervisor/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/factorio-supervisor ./cmd/supervisor

# --- Runtime image -----------------------------------------------------------
# The game itself is no longer baked in: the supervisor downloads the headless
# build onto the /factorio volume on first boot and reuses it thereafter.
FROM debian:stable-slim

RUN apt-get -q update \
    && DEBIAN_FRONTEND=noninteractive apt-get -qy install --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# /factorio is the volume mount point and starts empty; the supervisor seeds it
# from /opt/defaults on every boot, which keeps config and public.json
# image-authoritative (changing them is a redeploy).
RUN mkdir -p /factorio
WORKDIR /factorio

COPY --from=supervisor-build /out/factorio-supervisor /usr/local/bin/factorio-supervisor
COPY ./config /opt/defaults/config
COPY ./mods /opt/defaults/mods

EXPOSE 34197/udp 27015/tcp 8080/tcp
ENTRYPOINT ["/usr/local/bin/factorio-supervisor", "run"]
