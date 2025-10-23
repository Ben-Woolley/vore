#!/bin/bash
set -e

# Download dependencies if not already pulled
go mod vendor

# Build image (uses vendor modules)
docker build . -t ben-woolley/vore:latest
