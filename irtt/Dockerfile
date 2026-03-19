# Stage 1: build
FROM golang:1.21-alpine AS builder

ENV GO111MODULE=on
ENV CGO_ENABLED=0

# Install dependencies
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /build

# Fetch and build irtt
RUN go install github.com/heistp/irtt/cmd/irtt@latest

# Stage 2: minimal runtime
FROM alpine:latest

# Install CA certificates if HTTPS needed (optional)
RUN apk add --no-cache ca-certificates

# Copy the irtt binary from builder
COPY --from=builder /go/bin/irtt /usr/local/bin/irtt

# Set entrypoint
ENTRYPOINT ["irtt"]