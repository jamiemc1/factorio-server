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
FROM factoriotools/factorio:2.1.12
WORKDIR /factorio

COPY --from=supervisor-build /out/factorio-supervisor /usr/local/bin/factorio-supervisor
COPY ./files/*.sh /
COPY ./config config
COPY ./mods mods

ENTRYPOINT ["/usr/local/bin/factorio-supervisor", "run"]
