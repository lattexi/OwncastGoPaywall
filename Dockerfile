# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o server ./cmd/server

# Runtime stage
FROM alpine:3.19

WORKDIR /app

# Install runtime dependencies
# docker-cli is needed for Docker socket communication
RUN apk add --no-cache ca-certificates tzdata docker-cli

# Copy binary from builder
COPY --from=builder /app/server .

# Copy web assets
COPY --from=builder /app/web ./web

# Note: Running as root to allow Docker socket access
# For production, consider using a Docker socket proxy or docker group configuration

# Expose port
EXPOSE 3000

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:3000/health || exit 1

# Run the application
CMD ["./server"]
