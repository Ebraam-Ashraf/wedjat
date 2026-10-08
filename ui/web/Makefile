SHELL := /bin/bash

# Web UI Makefile
WEB_DIR := $(CURDIR)
ROOT_DIR := $(abspath $(WEB_DIR)/../..)
SERVER_DIST_DIR := $(ROOT_DIR)/ui/server/httpd/dist

.PHONY: all help dev build test clean

all: build

help:
	@echo "Wedjat Web UI — Makefile"
	@echo "  make dev      Start Vite dev server (HMR)"
	@echo "  make build    Build production assets into server dist folder"
	@echo "  make test     Run web frontend tests"
	@echo "  make clean    Remove node_modules and build artifacts"

dev:
	@echo "==> Starting Vite development server"
	@npm ci --prefix "$(WEB_DIR)"
	@npm run dev

build:
	@echo "==> Building Web UI for production"
	@npm ci --prefix "$(WEB_DIR)"
	@npm run build
	@echo "Built UI output to: $(SERVER_DIST_DIR)"

test:
	@echo "==> Running Web UI tests"
	@echo "(No tests configured yet. Add to package.json scripts when ready.)"
	@npm run test --if-present

clean:
	@echo "==> Cleaning Web UI"
	@rm -rf node_modules
	@rm -rf $(SERVER_DIST_DIR)
	@echo "Clean."
