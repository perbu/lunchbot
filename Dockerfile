# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
# RUN apk add --no-cache gcc musl-dev sqlite-dev

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -o lunchbot .

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache sqlite ca-certificates tzdata

# Create app directory and user
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

WORKDIR /app

# Create data directory for SQLite database
RUN mkdir -p /app/data && chown -R appuser:appgroup /app

# Copy binary from builder stage
COPY --from=builder /app/lunchbot .

# Copy migrations
COPY --from=builder /app/migrations ./migrations

# Copy config example (user should mount their own config)
COPY --from=builder /app/config.yaml.example .

# Switch to non-root user
USER appuser

# Expose any ports if needed (this bot uses WebSocket, so no HTTP port needed)
# EXPOSE 8080

# Set environment variables
ENV DB_PATH=/app/data/lunchbot.db

# Create volume for persistent storage
VOLUME ["/app/data"]

# Run the application
CMD ["./lunchbot"]