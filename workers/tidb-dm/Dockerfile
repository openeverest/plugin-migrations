# Worker image for the tidb-dm engine: TiDB Data Migration (dm-master,
# dm-worker and dmctl from PingCAP's image) plus the agent the plugin backend
# drives. The agent runs dm-master and dm-worker as child processes; their
# state and the dump live on the worker's volume (DATA_DIR).
ARG DM_IMAGE=pingcap/dm:v8.5.8

FROM golang:1.25-alpine AS agent
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tidb-dm-agent .

FROM ${DM_IMAGE}
RUN mkdir -p /data && chown 65532:65532 /data
COPY --from=agent /tidb-dm-agent /usr/local/bin/tidb-dm-agent
ENV DATA_DIR=/data DM_BIN_DIR=/ HOME=/tmp
# Numeric so Kubernetes can enforce runAsNonRoot; the volume is group-owned
# by 65532 (the pod's fsGroup).
USER 65532:65532
EXPOSE 2243
ENTRYPOINT ["/usr/local/bin/tidb-dm-agent"]
