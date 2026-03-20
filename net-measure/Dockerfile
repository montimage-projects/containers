# Stage 1: build
FROM golang:1.21-alpine AS builder

ENV GO111MODULE=on
ENV CGO_ENABLED=0

# Install dependencies
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /build

# copy and build
COPY . .
RUN GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o net-measure

# Stage 2: minimal runtime
FROM alpine:latest

# Install CA certificates if HTTPS needed (optional)
RUN apk add --no-cache ca-certificates

# Copy the irtt binary from builder
COPY --from=builder /build/net-measure /usr/local/bin/net-measure

# Set entrypoint
ENTRYPOINT ["net-measure"]