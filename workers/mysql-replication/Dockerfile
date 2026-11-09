# Worker image for the mysql-replication engine: MySQL Shell (mysqlsh), which
# copies the data, and the agent the plugin backend drives. The official MySQL
# image ships MySQL Shell; the server in it is never started.
ARG MYSQL_IMAGE=mysql:8.4

FROM golang:1.25-alpine AS agent
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mysql-replication-agent .

FROM ${MYSQL_IMAGE}
# /etc/my.cnf points clients at a local server socket; MySQL Shell must only
# use the connections the agent gives it.
RUN rm -f /etc/my.cnf && mkdir -p /work && chown 999:999 /work
ENV WORK_DIR=/work HOME=/work LC_ALL=C.UTF-8
COPY --from=agent /mysql-replication-agent /usr/local/bin/mysql-replication-agent
# Numeric so Kubernetes can enforce runAsNonRoot (uid 999 is "mysql").
USER 999:999
EXPOSE 2243
ENTRYPOINT ["/usr/local/bin/mysql-replication-agent"]
