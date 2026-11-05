# Worker image for the postgres-logical engine: PostgreSQL client tools
# (psql, pg_dump, pg_restore) and the agent the plugin backend drives.
# The newest client dumps and restores every older server version.
ARG POSTGRES_IMAGE=postgres:18-alpine

FROM golang:1.25-alpine AS agent
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /postgres-logical-agent .

FROM ${POSTGRES_IMAGE}
RUN mkdir -p /work && chown 70:70 /work
ENV WORK_DIR=/work
COPY --from=agent /postgres-logical-agent /usr/local/bin/postgres-logical-agent
# Numeric so Kubernetes can enforce runAsNonRoot (uid 70 is "postgres" on Alpine).
USER 70:70
EXPOSE 2242
ENTRYPOINT ["/usr/local/bin/postgres-logical-agent"]
