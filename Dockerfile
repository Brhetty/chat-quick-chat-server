# Multi-stage build to produce a small runtime image
FROM golang:1.22 AS builder
WORKDIR /app

# Go module download
COPY go.mod go.sum ./
RUN go mod download

# Build server
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server

# Minimal runtime image with certificates
FROM debian:bookworm-slim
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

# Create data directories (will be bind-mounted by docker-compose)
RUN mkdir -p /app/data /app/storage/chat-media

COPY --from=builder /app/server /app/server

EXPOSE 8080
CMD ["/app/server"]
