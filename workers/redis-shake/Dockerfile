# Worker image for the redis-shake engine: the agent the plugin backend drives
# and RedisShake, which copies and replicates the data. Distroless: the agent
# runs RedisShake directly, no shell is needed.
FROM golang:1.25-alpine AS agent
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /redis-shake-agent . && mkdir -p /work

FROM alpine:3.22 AS shake
ARG TARGETARCH
ARG REDIS_SHAKE_VERSION=4.7.0
ARG REDIS_SHAKE_SHA256_AMD64=920b9d256959162b9be972e0b9df43334f473858d21a84acf39d3b1e96eabe26
ARG REDIS_SHAKE_SHA256_ARM64=7fb2e992954c0ef16474ef737d5933434e2b6167415e620a8bb8667dffd57695
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) sha="${REDIS_SHAKE_SHA256_AMD64}" ;; \
      arm64) sha="${REDIS_SHAKE_SHA256_ARM64}" ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    wget -qO /shake.tar.gz "https://github.com/tair-opensource/RedisShake/releases/download/v${REDIS_SHAKE_VERSION}/redis-shake-v${REDIS_SHAKE_VERSION}-linux-${TARGETARCH}.tar.gz"; \
    echo "${sha}  /shake.tar.gz" | sha256sum -c -; \
    tar -xzf /shake.tar.gz -C / ./redis-shake

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=agent /redis-shake-agent /usr/local/bin/redis-shake-agent
COPY --from=shake /redis-shake /usr/local/bin/redis-shake
COPY --from=agent --chown=65532:65532 /work /work
ENV WORK_DIR=/work
# Numeric so Kubernetes can enforce runAsNonRoot.
USER 65532:65532
EXPOSE 2243
ENTRYPOINT ["/usr/local/bin/redis-shake-agent"]
