#!/bin/bash
set -e

# Download dependencies if not already pulled
go mod vendor

# Build image (uses vendor modules)
docker build . -t localhost:5000/ben-woolley/vore:latest
