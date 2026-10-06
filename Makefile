SHELL := /bin/bash
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c

ROOT_DIR := $(CURDIR)
DAEMON_DIR := $(ROOT_DIR)/daemon
UI_DIR := $(ROOT_DIR)/ui/web
FRONTEND_DIR := $(UI_DIR)/frontend
DIST_DIR := $(ROOT_DIR)/dist
STAGE_DIR := $(DIST_DIR)/.stage

GOARCH ?= amd64
RELEASE_NAME := wedjat-linux-$(GOARCH)
ARCHIVE := $(DIST_DIR)/$(RELEASE_NAME).tar.gz
.DEFAULT_GOAL := build

.PHONY: build release install uninstall clean help

help:
	@printf '%s\n' \
		'Wedjat developer commands' \
		'  sudo make                 Build the release artifacts' \
		'  sudo make install         Build and install the local release' \
		'  sudo make uninstall       Uninstall Wedjat, preserving build files' \
		'  sudo make clean           Uninstall and remove all build artifacts' \
		'  sudo make help            Show this help'

build:
	command -v go >/dev/null || { echo 'build requires Go' >&2; exit 1; }
	command -v npm >/dev/null || { echo 'build requires npm' >&2; exit 1; }
	command -v node >/dev/null || { echo 'build requires node' >&2; exit 1; }
	command -v tar >/dev/null || { echo 'build requires tar' >&2; exit 1; }

	$(MAKE) -C "$(DAEMON_DIR)" GOARCH=$(GOARCH) release
	( cd "$(FRONTEND_DIR)" && npm ci && npm run build )

	rm -rf "$(STAGE_DIR)"
	mkdir -p "$(STAGE_DIR)/ui/frontend"
	install -m 0755 "$(DAEMON_DIR)/dist/wedjatd" "$(STAGE_DIR)/wedjatd"
	install -m 0755 "$(DAEMON_DIR)/dist/wedjat-uninstall" "$(STAGE_DIR)/wedjat-uninstall"
	install -m 0644 "$(DAEMON_DIR)/dist/config.yaml" "$(STAGE_DIR)/config.yaml"
	install -m 0644 "$(DAEMON_DIR)/dist/wedjatd.service" "$(STAGE_DIR)/wedjatd.service"
	install -d -m 0755 "$(STAGE_DIR)/ebpf"
	install -m 0644 "$(DAEMON_DIR)"/ebpf/build/*.bpf.o "$(STAGE_DIR)/ebpf/"

	# Build a production copy without changing the development UI source.
	sed \
		-e "s|const baseDir = .*|const socketPath = process.env.WEDJAT_SOCKET_PATH || '/run/wedjat/wedjat.sock';\\nconst dataDir = process.env.WEDJAT_DATA_DIR || '/var/lib/wedjat';|" \
		-e "/const socketPath = path.join(baseDir, 'run', 'wedjat.sock');/d" \
		-e "/const dataDir = path.join(baseDir, 'var', 'lib', 'wedjat');/d" \
		-e "s|path.join(__dirname, 'frontend')|path.join(__dirname, 'frontend', 'dist')|" \
		"$(UI_DIR)/server.js" > "$(STAGE_DIR)/ui/server.js"
	cp "$(UI_DIR)/package.json" "$(UI_DIR)/package-lock.json" "$(STAGE_DIR)/ui/"
	cp -R "$(FRONTEND_DIR)/dist" "$(STAGE_DIR)/ui/frontend/"
	( cd "$(STAGE_DIR)/ui" && npm ci --omit=dev )

	# Embed the UI runtime behind the top-level wedjat command.
	cat > "$(STAGE_DIR)/wedjat" <<'LAUNCHER'
	#!/bin/sh
	set -eu

	case "$${1:-}" in
	  uninstall)
	    [ "$${#}" -eq 1 ] || {
	      echo 'Usage: wedjat uninstall' >&2
	      exit 2
	    }
	    if [ "$$(id -u)" -eq 0 ]; then
	      exec /usr/local/bin/wedjat-uninstall
	    fi
	    command -v sudo >/dev/null 2>&1 || {
	      echo 'wedjat: uninstall requires root; run sudo wedjat uninstall' >&2
	      exit 1
	    }
	    exec sudo /usr/local/bin/wedjat-uninstall
	    ;;
  --help|-h)
	    printf '%s\n' 'Usage: wedjat [uninstall]' '  wedjat            Open the dashboard' '  wedjat uninstall  Remove Wedjat'
	    exit 0
	    ;;
  '')
	    ;;
  *)
	    echo 'Usage: wedjat [uninstall]' >&2
	    exit 2
	    ;;
esac

	command -v node >/dev/null 2>&1 || {
	  echo 'wedjat: node.js is required to open the dashboard' >&2
	  exit 1
	}
	runtime_dir=$$(mktemp -d "$${TMPDIR:-/tmp}/wedjat.XXXXXX")
	cleanup() { rm -rf -- "$$runtime_dir"; }
	trap cleanup EXIT INT TERM
	awk '/^__WEDJAT_PAYLOAD__$$/ { payload=1; next } payload { print }' "$$0" | tar -xzf - -C "$$runtime_dir"
	cd "$$runtime_dir"
	exec node server.js "$$@"
	exit 0
	__WEDJAT_PAYLOAD__
	LAUNCHER
	chmod 0755 "$(STAGE_DIR)/wedjat"
	tar -C "$(STAGE_DIR)/ui" -czf "$(STAGE_DIR)/wedjat-ui.tar.gz" server.js package.json package-lock.json node_modules frontend/dist
	cat "$(STAGE_DIR)/wedjat-ui.tar.gz" >> "$(STAGE_DIR)/wedjat"
	rm -f "$(STAGE_DIR)/wedjat-ui.tar.gz"

	rm -f "$(ARCHIVE)" "$(ARCHIVE).sha256"
	tar -C "$(STAGE_DIR)" -czf "$(ARCHIVE)" wedjat wedjatd wedjat-uninstall config.yaml wedjatd.service ebpf
	( cd "$(DIST_DIR)" && sha256sum "$(notdir $(ARCHIVE))" > "$(notdir $(ARCHIVE)).sha256" )
	rm -rf "$(STAGE_DIR)"
	printf 'Created %s\n' "$(ARCHIVE)"
	printf 'Created %s\n' "$(ARCHIVE).sha256"

release: build

install: build
	sudo "$(ROOT_DIR)/scripts/install.sh" --local "$(DIST_DIR)"

uninstall:
	if [ "$${EUID:-$$(id -u)}" -eq 0 ]; then
		"$(ROOT_DIR)/scripts/uninstall.sh"
	else
		sudo "$(ROOT_DIR)/scripts/uninstall.sh"
	fi

clean:
	if [ "$${EUID:-$$(id -u)}" -eq 0 ]; then
		systemctl disable --now wedjatd 2>/dev/null || true
		rm -rf /usr/local/bin/wedjat /usr/local/bin/wedjatd /usr/local/bin/wedjat-uninstall
		rm -rf /usr/local/lib/wedjat /etc/wedjat /var/lib/wedjat
		rm -f /etc/systemd/system/wedjatd.service
		systemctl daemon-reload 2>/dev/null || true
	else
		sudo systemctl disable --now wedjatd 2>/dev/null || true
		sudo rm -rf /usr/local/bin/wedjat /usr/local/bin/wedjatd /usr/local/bin/wedjat-uninstall
		sudo rm -rf /usr/local/lib/wedjat /etc/wedjat /var/lib/wedjat
		sudo rm -f /etc/systemd/system/wedjatd.service
		sudo systemctl daemon-reload 2>/dev/null || true
	fi
	$(MAKE) -C "$(DAEMON_DIR)" clean
	rm -rf "$(DIST_DIR)"
