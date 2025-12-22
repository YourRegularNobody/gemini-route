FROM alpine:latest

# Install iproute2 (for 'ip' command) and ca-certificates (for HTTPS)
RUN apk add --no-cache iproute2 ca-certificates

# Copy the binary from GoReleaser's build context
COPY gemini-route /usr/bin/gemini-route

# Set entrypoint
ENTRYPOINT ["/usr/bin/gemini-route"]
