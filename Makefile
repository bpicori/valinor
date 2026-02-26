.PHONY: build run run-gateway run-tui test clean deps fmt lint install

BINARY := bin/valinor

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

test:
	go test ./...

clean:
	rm -f $(BINARY)
	go clean

deps:
	go mod download
	go mod tidy

fmt:
	go fmt ./...

lint:
	go vet ./...

install: build
	go install ./cmd/valinor
