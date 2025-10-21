# Multi-stage Docker build for CASDC - Complete Active Directory Server Controller
# This creates a minimal, secure container with all features embedded in a single static binary

# Stage 1: Build environment with all dependencies
FROM golang:1.23-alpine AS builder

# Install build dependencies for static compilation
RUN apk add --no-cache \
    gcc \
    musl-dev \
    sqlite-dev \
    make \
    git \
    ca-certificates \
    tzdata

# Set working directory
WORKDIR /build

# Copy go module files for dependency caching
COPY go.mod go.sum ./
RUN go mod download

# Copy entire source code
COPY . .

# Build the static binary with all optimizations
# CGO_ENABLED=0 for pure static binary
# -ldflags for stripping debug info and setting version
RUN CGO_ENABLED=0 GOOS=linux go build \
    -a -installsuffix cgo \
    -ldflags="-w -s -X main.Version=$(git describe --tags --always) \
    -X main.BuildTime=$(date -u +%Y-%m-%d_%H:%M:%S) \
    -X main.GitCommit=$(git rev-parse HEAD)" \
    -o casdc \
    cmd/casdc/main.go

# Stage 2: Development environment with hot reload
FROM golang:1.23 AS development

# Install development dependencies
RUN apt-get update && apt-get install -y \
    nginx \
    postfix \
    dovecot-core \
    bind9 \
    isc-dhcp-server \
    samba \
    clamav \
    fail2ban \
    openvpn \
    wireguard-tools \
    strongswan \
    pure-ftpd \
    git \
    make \
    vim \
    curl \
    net-tools \
    iputils-ping \
    dnsutils \
    tcpdump \
    strace \
    htop \
    postgresql-client \
    mariadb-client \
    redis-tools \
    && rm -rf /var/lib/apt/lists/*

# Install development tools
RUN go install github.com/cosmtrek/air@latest && \
    go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest && \
    go install github.com/swaggo/swag/cmd/swag@latest

WORKDIR /app

# Copy source code
COPY . .

# Create required directories
RUN mkdir -p /etc/casdc /var/lib/casdc /var/log/casdc /var/www/default /tmp/casdc

# Set environment for development
ENV CASDC_DEBUG=true \
    CASDC_LOG_LEVEL=debug \
    CASDC_DEVELOPMENT_MODE=true

# Expose all service ports for development
EXPOSE 80 443 25 465 587 110 995 143 993 53/tcp 53/udp 67/udp 137/udp 138/udp 139 445 \
       1194/udp 51820/udp 500/udp 4500/udp 21 20 873 3000 8080 9090

# Use air for hot reload in development
CMD ["air", "-c", ".air.toml"]

# Stage 3: Testing environment with all test dependencies
FROM golang:1.23-alpine AS testing

# Install test dependencies
RUN apk add --no-cache \
    gcc \
    musl-dev \
    sqlite-dev \
    make \
    git \
    chromium \
    chromium-chromedriver \
    nodejs \
    npm

WORKDIR /test

# Copy source and test files
COPY . .

# Download test dependencies
RUN go mod download

# Install Node.js test dependencies for E2E testing
RUN npm install --global \
    puppeteer \
    @playwright/test \
    newman

# Run tests with coverage
CMD ["make", "test"]

# Stage 4: Production runtime - minimal container
FROM alpine:3.19 AS runtime

# Install minimal runtime dependencies
# These are for external service integration, not embedded in binary
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    nginx \
    postfix \
    bind \
    samba \
    clamav \
    fail2ban \
    openvpn \
    wireguard-tools \
    strongswan \
    && rm -rf /var/cache/apk/*

# Create casdc user and group for security
RUN addgroup -g 1000 -S casdc && \
    adduser -u 1000 -S casdc -G casdc

# Copy static binary from builder
COPY --from=builder /build/casdc /usr/local/bin/casdc

# Copy timezone data
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Create required directories with proper permissions
RUN mkdir -p /etc/casdc /var/lib/casdc /var/log/casdc /var/www/default /tmp/casdc /mnt/backups/casdc && \
    chown -R casdc:casdc /etc/casdc /var/lib/casdc /var/log/casdc /var/www/default /tmp/casdc && \
    chmod 700 /var/lib/casdc && \
    chmod 755 /etc/casdc /var/log/casdc /var/www/default

# Use tmpfs for temporary files to minimize disk writes
VOLUME ["/var/lib/casdc", "/etc/casdc", "/var/log/casdc", "/mnt/backups/casdc"]

# Environment variables for container deployment
ENV CASDC_CONTAINER=true \
    CASDC_DATA_DIR=/var/lib/casdc \
    CASDC_CONFIG_DIR=/etc/casdc \
    CASDC_LOG_DIR=/var/log/casdc

# Health check for container orchestration
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD casdc diagnostic || exit 1

# Expose standard service ports
# HTTP/HTTPS
EXPOSE 80 443
# SMTP/Submission/SMTPS
EXPOSE 25 465 587
# POP3/POP3S
EXPOSE 110 995
# IMAP/IMAPS
EXPOSE 143 993
# DNS
EXPOSE 53/tcp 53/udp
# DHCP
EXPOSE 67/udp
# NetBIOS/SMB
EXPOSE 137/udp 138/udp 139 445
# VPN (OpenVPN/WireGuard/IPSec)
EXPOSE 1194/udp 51820/udp 500/udp 4500/udp
# FTP
EXPOSE 21 20
# Rsync
EXPOSE 873

# Switch to non-root user for security (commented out for system service management)
# USER casdc

# Set the entrypoint to the CASDC binary
ENTRYPOINT ["/usr/local/bin/casdc"]

# Default command (can be overridden)
CMD []

# Stage 5: Debug environment with all debugging tools
FROM runtime AS debug

# Install debugging tools
RUN apk add --no-cache \
    bash \
    vim \
    curl \
    wget \
    net-tools \
    bind-tools \
    tcpdump \
    strace \
    gdb \
    htop \
    iotop \
    iftop \
    nmap \
    openssl \
    postgresql-client \
    mariadb-client \
    redis \
    busybox-extras

# Keep container running for debugging
CMD ["/bin/bash"]