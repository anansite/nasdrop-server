#!/bin/sh
set -e
export GOPROXY=https://goproxy.cn,direct
go mod tidy
CGO_ENABLED=0 go build -ldflags="-s -w" -o nasdrop-server ./cmd/server
ls -lh nasdrop-server
