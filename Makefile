.PHONY: build run run-gateway run-tui run-tui-remote run-telegram monitor-processes sqlite-repl test clean deps fmt lint install clean-valinor-dir

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

monitor-processes:
	@while true; do \
		clear; \
		echo "Monitoring valinor processes (refresh: 1s, exit: Ctrl+C)"; \
		echo ""; \
		ps -axo pid,ppid,%cpu,rss,etime,command | awk '\
			BEGIN { \
				found = 0; \
				printf "%-8s %-8s %-8s %-10s %-12s %s\n", "PID", "PPID", "CPU%", "MEM(MB)", "ELAPSED", "COMMAND"; \
			} \
			/-mode=(gateway|tui|telegram)/ && /valinor/ { \
				found = 1; \
				printf "%-8s %-8s %-8s %-10.1f %-12s %s\n", $$1, $$2, $$3, $$4 / 1024, $$5, substr($$0, index($$0, $$6)); \
			} \
			END { \
				if (!found) print "No valinor gateway, tui, or telegram processes are running."; \
			}'; \
		sleep 1; \
	done

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
