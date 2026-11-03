# Expects dist/main.js to be pre-built (npm run build) in the build context.
FROM golang:1.25-alpine AS backend-builder

WORKDIR /app

COPY backend/go.mod backend/go.sum* ./
RUN go mod download

COPY backend/ ./
COPY dist/main.js ./dist/main.js

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o server .

FROM alpine:3.21

RUN apk --no-cache add ca-certificates

COPY --from=backend-builder /app/server /usr/local/bin/server

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["server"]
