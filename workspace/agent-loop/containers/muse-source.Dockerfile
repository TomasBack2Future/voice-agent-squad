# Build explicitly, then select its sha256 image ID in the launch configuration.
FROM golang:1.26.5-bookworm AS go
FROM node:22.23.2-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git gh python3 python3-pip python3-venv make gcc libc6-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH
ENV GOTOOLCHAIN=local
