#!/bin/bash

# Build vore to use in docker image
go build .

# Build image
docker build -t ben-woolley/vore:latest

# Save image as tar file for import onto server
docker save -o . ben-woolley/vore:latest