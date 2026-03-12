#!/bin/sh

set -e

GOOS=darwin go clean -testcache
GOOS=darwin go test -v -cover ./... -coverprofile=./.github/cover.out
GOOS=darwin go tool cover -html=./.github/cover.out
