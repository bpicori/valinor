.PHONY: build run run-gateway run-tui run-tui-remote run-telegram sqlite-repl test clean deps fmt lint install clean-valinor-dir

BINARY := bin/valinor
VALINOR_DIR ?= $(HOME)/.valinor
SESSIONS_DIR ?= $(VALINOR_DIR)/sessions
METADATA_DB ?= $(SESSIONS_DIR)/metadata.db

# Build the binary
build:
	go build -o $(BINARY) ./cmd/valinor

# Run both gateway and tui (for local development, starts gateway in background then tui)
run: build
	@echo "Starting gateway in background..."
	@./$(BINARY) -mode=gateway &
	@sleep 1
	@echo "Starting TUI..."
	@./$(BINARY) -mode=tui || true
	@echo "Stopping gateway..."
	@pkill -f "$(BINARY) -mode=gateway" 2>/dev/null || true

# Run only the gateway (session core + WebSocket server)
run-gateway: build
	./$(BINARY) -mode=gateway

# Run only the TUI (connects to existing gateway)
run-tui: build
	./$(BINARY) -mode=tui

# Run TUI connecting to a specific gateway URL
run-tui-remote: build
	./$(BINARY) -mode=tui -gateway-url=ws://$(REMOTE_GATEWAY)/ws

run-telegram: build
	./$(BINARY) -mode=telegram

sqlite-repl:
	@command -v sqlite3 >/dev/null || (echo "sqlite3 CLI not found in PATH" && exit 1)
	@mkdir -p "$(SESSIONS_DIR)"
	@sqlite3 "$(METADATA_DB)" -cmd ".headers on" -cmd ".mode column"

clean-valinor-dir:
	rm -rf ~/.valinor/*
	rm -rf ~/.valinor/.[!.]*
	rm -rf ~/.valinor/..?*

test:
	go test ./...

deps:
	go mod download
	go mod tidy

fmt:
	go fmt ./...

lint:
	go vet ./...

install: build
	go install ./cmd/valinor
