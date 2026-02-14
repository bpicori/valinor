.PHONY: build run test clean deps fmt lint install

BINARY := bin/valinor

build:
	go build -o $(BINARY) .

run: build
	./$(BINARY)

test:
	go test ./...

clean:
	rm -f $(BINARY)

deps:
	go mod download
	go mod tidy

fmt:
	go fmt ./...

lint:
	go vet ./...

install: build
	go install .
