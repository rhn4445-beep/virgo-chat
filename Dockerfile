# Build stage: compile the Go gateway for Linux
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build statically linked binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o gateway main.go

# Runtime stage: minimal Alpine container
FROM alpine:latest

WORKDIR /app
RUN apk --no-cache add ca-certificates tzdata

# Copy compiled gateway and web assets
COPY --from=builder /app/gateway .
COPY --from=builder /app/index.html .
COPY --from=builder /app/manifest.json .
COPY --from=builder /app/sw.js .
COPY --from=builder /app/icon.png .

# Ensure uploads directory exists
RUN mkdir -p uploads

# Port 8080 exposed for HTTP/WebSocket traffic
EXPOSE 8080

CMD ["./gateway"]
