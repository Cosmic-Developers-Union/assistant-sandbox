PREFIX ?= $(HOME)/.local

.PHONY: guest build install clean lint test docker-test

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

# 所有测试在 Docker 内执行并以 cgroup 硬限 512MB(含 swap),避免宿主机内存压力。
# 用法:make docker-test                          # 全量 go test ./...
# 	或:make docker-test TEST_ARGS="./abi/... -run TestXxx"  # 定向
.PHONY: docker-test
docker-test:
	docker run --rm --init --memory=512m --memory-swap=512m --cpus=2 \
		-v "$$(pwd)":/work -w /work \
		-v assistant-sandbox-go-build:/go-build \
		-v assistant-sandbox-go-mod:/go/pkg/mod \
		-e GOFLAGS=-p=1 \
		-e GOMEMLIMIT=320MiB \
		-e GOGC=20 \
		-e GOCACHE=/go-build \
		golang:1.27 \
		sh -c 'make guest && go test -count=1 -timeout=20m $(if $(TEST_ARGS),$(TEST_ARGS),./...)'; \
	status=$$?; \
	chown --quiet "$$(id -u):$$(id -g)" assistant-bash.wasm assistant-bash.wasm.gz 2>/dev/null || true; \
	exit $$status
