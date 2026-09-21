# ==============================================================================
# Build Stage
# ==============================================================================
FROM golang:1.27-alpine AS builder

WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/db-saver main.go

# ==============================================================================
# Runtime Stage
# ==============================================================================
FROM alpine:3.21

# Install runtime tools: rclone, sqlite3, pg_dump (postgresql-client), ca-certificates, tzdata
RUN apk add --no-cache \
    rclone \
    sqlite \
    postgresql-client \
    ca-certificates \
    tzdata \
    curl

WORKDIR /app

# Copy binary and static/templates
COPY --from=builder /bin/db-saver /app/db-saver
COPY web /app/web

# Create default storage and configuration directories
RUN mkdir -p /data /config /data/tmp

ENV PORT=8080 \
    DATA_DIR=/data \
    RCLONE_CONFIG_PATH=/config/rclone.conf \
    TMP_BACKUP_DIR=/data/tmp

EXPOSE 8080

ENTRYPOINT ["/app/db-saver"]
