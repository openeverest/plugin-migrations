# Worker image for the pgcopydb engine: pgcopydb plus the agent that the
# plugin-migrations backend drives over HTTP.
# Upstream pgcopydb image. Change replication in v0.18 stalls and has open
# data-loss issues fixed on upstream main; move to the release with the fixes
# (v0.19) as soon as it ships. Build with
# --build-arg PGCOPYDB_IMAGE=ghcr.io/dimitri/pgcopydb:latest to test upstream main.
ARG PGCOPYDB_IMAGE=dimitri/pgcopydb:v0.18
ARG PG_CLIENT_MAJOR=18

FROM golang:1.25-alpine AS agent
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pgcopydb-agent .

FROM ${PGCOPYDB_IMAGE}
ARG PG_CLIENT_MAJOR
USER root
# The upstream image ships PostgreSQL 16 client tools, and pg_dump refuses to
# dump a newer server. Newer clients dump and restore every older version.
RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends postgresql-client-${PG_CLIENT_MAJOR} >/dev/null \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /work \
 && chown 999:102 /work
ENV PATH=/usr/lib/postgresql/${PG_CLIENT_MAJOR}/bin:$PATH \
    WORK_DIR=/work \
    XDG_DATA_HOME=/work/data
COPY --from=agent /pgcopydb-agent /usr/local/bin/pgcopydb-agent
# Numeric so Kubernetes can enforce runAsNonRoot (the upstream user is "docker").
USER 999:102
EXPOSE 2242
ENTRYPOINT ["/usr/local/bin/pgcopydb-agent"]
