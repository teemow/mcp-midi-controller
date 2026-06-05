# mcp-midi-controller build targets.
#
# The "signalwave" web UI (web/, Vite + React) is built into
# internal/webui/dist, which is committed and consumed by go:embed so the Go
# binary builds from a clean checkout without Node. Run `make web` after
# changing anything under web/ and commit the regenerated dist.

WEB_DIR := web
EMBED_DIR := internal/webui/dist

.PHONY: all build web web-install go-build go-install deploy restart status logs test lint check-web-clean clean

all: build

## build: build the web UI then the Go binary
build: web go-build

## web: build the signalwave SPA into the committed embed dir
web:
	cd $(WEB_DIR) && npm ci && npm run build

## web-install: install web deps only (for local dev)
web-install:
	cd $(WEB_DIR) && npm ci

## go-build: build the daemon (consumes the committed embed dir)
go-build:
	go build ./...

## go-install: install the daemon into ~/.go/bin (where the systemd unit looks)
go-install:
	GOBIN=$(HOME)/.go/bin go install ./cmd/mcp-midi-controller

## deploy: build + install + (re)start the systemd user service (idempotent;
## first install or rolling out a new build). See scripts/deploy.sh.
deploy:
	./scripts/deploy.sh

## restart: restart the running systemd user service
restart:
	systemctl --user restart mcp-midi-controller.service

## status: show the systemd user service status
status:
	systemctl --user --no-pager status mcp-midi-controller.service

## logs: follow the daemon journal
logs:
	journalctl --user -u mcp-midi-controller.service -f

## test: run the Go test suite
test:
	go test -race ./...

## lint: run golangci-lint and the web type-check
lint:
	golangci-lint run
	cd $(WEB_DIR) && npm run lint

## check-web-clean: rebuild the SPA and fail if the committed embed dir drifted
## (used in CI to guarantee internal/webui/dist matches web/).
check-web-clean: web
	@if ! git diff --quiet -- $(EMBED_DIR); then \
		echo "ERROR: $(EMBED_DIR) is out of date. Run 'make web' and commit the result."; \
		git --no-pager diff --stat -- $(EMBED_DIR); \
		exit 1; \
	fi
	@echo "$(EMBED_DIR) is up to date."

## clean: remove build artifacts
clean:
	rm -rf $(WEB_DIR)/node_modules $(WEB_DIR)/public/docs
	go clean
