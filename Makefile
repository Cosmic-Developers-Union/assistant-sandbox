PREFIX ?= $(HOME)/.local

.PHONY: guest build install clean lint test

guest:
	go run ./cmd/assistant-sandbox-build

build: guest
	CGO_ENABLED=0 go build -trimpath -o assistant-sandbox ./cmd/assistant-sandbox

install: build
	install -d "$(PREFIX)/bin" "$(PREFIX)/share/assistant-sandbox"
	install -m 0755 assistant-sandbox "$(PREFIX)/bin/assistant-sandbox"
	install -m 0644 assistant-bash.wasm.gz "$(PREFIX)/share/assistant-sandbox/assistant-bash.wasm.gz"

clean:
	rm -f assistant-sandbox assistant-sandbox.exe assistant-bash.wasm assistant-bash.wasm.gz
	rm -rf dist

lint:
	golangci-lint run

test: guest
	go test ./...
