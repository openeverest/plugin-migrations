# Worker image for the mssql-log-shipping engine: the agent the plugin
# backend drives. The SQL Servers back up to and restore from S3 themselves;
# the agent only issues T-SQL and cleans up the bucket. Distroless.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS agent
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /mssql-log-shipping-agent .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=agent /mssql-log-shipping-agent /usr/local/bin/mssql-log-shipping-agent
# Numeric so Kubernetes can enforce runAsNonRoot.
USER 65532:65532
EXPOSE 2243
ENTRYPOINT ["/usr/local/bin/mssql-log-shipping-agent"]
