# Worker image for the mysql-replication and mariadb-replication engines: the
# agent the plugin backend drives, MySQL Shell (mysqlsh), which copies MySQL
# data, and mydumper/myloader, which copy MariaDB data. The official MySQL
# image ships MySQL Shell; the server in it is never started.
ARG MYSQL_IMAGE=mysql:8.4

FROM golang:1.25-alpine AS agent
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mysql-replication-agent .

FROM alpine:3.22 AS mydumper
ARG TARGETARCH
ARG MYDUMPER_VERSION=1.0.19-1
ARG MYDUMPER_SHA256_AMD64=ab4b54e58da7a81cb6cde21e4866a17a8e68b1d204f1ed17767a87d72a46a552
ARG MYDUMPER_SHA256_ARM64=ddd4af85b871a13e023aed0b5f06a3fd0b4a490c78216ebfe1697e4c0127d99b
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) arch=x86_64; sha="${MYDUMPER_SHA256_AMD64}" ;; \
      arm64) arch=aarch64; sha="${MYDUMPER_SHA256_ARM64}" ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    wget -qO /mydumper.rpm "https://github.com/mydumper/mydumper/releases/download/v${MYDUMPER_VERSION}/mydumper-${MYDUMPER_VERSION}.el9.${arch}.rpm"; \
    echo "${sha}  /mydumper.rpm" | sha256sum -c -

FROM ${MYSQL_IMAGE}
COPY --from=mydumper /mydumper.rpm /tmp/mydumper.rpm
# /etc/my.cnf points clients at a local server socket; the copy tools must
# only use the connections the agent gives them.
RUN rpm -i /tmp/mydumper.rpm && rm /tmp/mydumper.rpm && mydumper --version && myloader --version \
    && rm -f /etc/my.cnf && mkdir -p /work && chown 999:999 /work
ENV WORK_DIR=/work HOME=/work LC_ALL=C.UTF-8
COPY --from=agent /mysql-replication-agent /usr/local/bin/mysql-replication-agent
# Numeric so Kubernetes can enforce runAsNonRoot (uid 999 is "mysql").
USER 999:999
EXPOSE 2243
ENTRYPOINT ["/usr/local/bin/mysql-replication-agent"]
