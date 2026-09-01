# Native development surface for the CLIProxyAPI backend fork.
# Go owns build, vet, and test; this file only composes them under the
# canonical verbs every workspace exposes.

SHELL := /bin/sh
.DEFAULT_GOAL := help
APPLY ?= N

.PHONY: help setup gen fmt fix check test

help: ## show the complete development surface
	@awk 'BEGIN{FS=":.*## "} /^[a-z][a-z-]*:.*## /{printf "  %-8s %s\n",$$1,$$2}' $(MAKEFILE_LIST)

setup: ## download and verify the declared module graph
	@go mod download
	@go mod verify

gen: ## regenerate declared sources and prove the module graph is unchanged
	@go generate ./...
	@go mod tidy
	@git diff --exit-code -- go.mod go.sum

fmt: ## report unformatted sources; APPLY=Y rewrites them
	@if [ "$(APPLY)" = "Y" ]; then gofmt -w .; else unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then printf 'ERROR: unformatted sources:\n%s\n' "$$unformatted" >&2; exit 1; fi; fi

fix: ## report vet findings; go vet has no autofixer, so APPLY changes nothing
	@go vet ./...

check: ## build and vet the complete module
	@go build ./...
	@go vet ./...

test: ## execute the complete Go test suite
	@go test ./...
