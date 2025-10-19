#!/bin/bash
set -e

# Download dependencies if not already pulled
go mod vendor

# Build image (uses vendor modules)
docker build . -t ben-woolley/vore:latest

# Save image as tar file for import onto server
docker save -o ./vore.tar ben-woolley/vore:latest