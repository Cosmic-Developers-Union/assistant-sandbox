PREFIX ?= $(HOME)/.local
HOST_GOOS := $(shell go env GOOS)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf dev)

ifeq ($(HOST_GOOS),linux)
CGO_ENABLED ?= 1
else
CGO_ENABLED ?= 0
endif

BUILD_FLAGS :=
GO_LDFLAGS := -X main.version=$(VERSION)
ifeq ($(CGO_ENABLED),1)
BUILD_FLAGS += -tags netgo,osusergo
ifeq ($(HOST_GOOS),linux)
BUILD_FLAGS += -ldflags "-linkmode external -extldflags -static $(GO_LDFLAGS)"
else
BUILD_FLAGS += -ldflags "$(GO_LDFLAGS)"
endif
else
BUILD_FLAGS += -ldflags "$(GO_LDFLAGS)"
endif

# 工具链声明:Go 版本以 go.mod 的 go 指令为唯一事实来源(CI 经 setup-go
# 的 go-version-file 安装同一版本,docker-test 使用对应补丁版本的镜像);
# golangci-lint 版本固定在 .github/workflows/test.yml 的 action 参数中;
# 客体工具链 = 同一 Go 工具链(GOOS=wasip1 GOARCH=wasm)+ gzip,无外部交叉编译器。
GOLANGCI_LINT_VERSION := v2.14.0

.PHONY: guest build release install clean lint test bench test-multicore docker-test toolchain isolated-check offline-check getting-started compat-s3 compat-ltp compat-s4 compat-python perf-languages

# B01:全新克隆仅依赖模块缓存即可构建(无 replace/无宿主绝对路径,
# 由 internal/metadata TestModuleResolvesWithoutLocalReplacements 锁定)。
isolated-check:
	@tmp=$$(mktemp -d); git clone -q "$$PWD" "$$tmp/iso"; \
	( cd "$$tmp/iso" && go build ./... && GOPROXY=off go build ./... ) \
		&& echo "isolated-check: 全新检出构建与离线解析通过" || { rm -rf "$$tmp"; exit 1; }; \
	rm -rf "$$tmp"

# B05:依赖与镜像预置后关闭下载通道,启动 VM、执行程序并关闭。
offline-check: build
	GOPROXY=off ./assistant-sandbox \
		--module assistant-bash.wasm.gz -c 'printf sandbox-ok' | grep -q sandbox-ok \
		&& echo "offline-check: 离线启动/执行/关闭通过"

toolchain:
	@expected=$$(sed -n 's/^go \([0-9.]*\)$$/\1/p' go.mod); actual=$$(go version | sed 's/^go version go//; s/ .*//'); if [ "$$actual" != "$$expected" ]; then echo "Go 版本与 go.mod 声明不一致: 本机 $$actual, 声明 $$expected" >&2; exit 1; fi; echo "Go $$actual(声明 $$expected)"; echo "golangci-lint $(GOLANGCI_LINT_VERSION)(CI action 参数)"; echo "客体工具链: 同一 Go 工具链 GOOS=wasip1 + gzip"

guest:
	go run ./cmd/assistant-sandbox-build

build: guest
	CGO_ENABLED=$(CGO_ENABLED) go build -trimpath $(BUILD_FLAGS) -o assistant-sandbox ./cmd/assistant-sandbox

# U12:生成可上传的 CLI/客体与摘要、模块 SBOM 清单。
release: build
	rm -rf dist
	mkdir -p dist
	cp assistant-sandbox dist/assistant-sandbox
	cp assistant-bash.wasm.gz dist/assistant-bash.wasm.gz
	cd dist && sha256sum assistant-sandbox assistant-bash.wasm.gz > SHA256SUMS
	go version -m assistant-sandbox > dist/SBOM.txt
	@echo "release: dist/SHA256SUMS 与 dist/SBOM.txt 已生成"

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

bench:
	go test ./executor/cpu/riscv64 -run '^$$' -bench . -benchtime=3s -count=3

test-multicore:
	go test -race ./instance -run '^TestMultiCoreTypicalConfigs$$' -count=1 -v

perf-languages:
	SANDBOX_PERF=1 go test ./internal/guest -run '^TestPerformancePython$$' -count=1 -v -timeout 10m
	SANDBOX_PERF=1 go test ./internal/guest -run '^TestPerformanceNodeJitless$$' -count=1 -v -timeout 10m
	SANDBOX_PERF=1 go test ./internal/guest -run '^TestPerformanceNodeStandard$$' -count=1 -v -timeout 10m
	SANDBOX_PERF=1 go test ./internal/guest -run '^TestPerformanceGo$$' -count=1 -v -timeout 10m

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
		golang:1.27.0 \
		sh -c 'make guest && go test -count=1 -timeout=20m $(if $(TEST_ARGS),$(TEST_ARGS),./...)'; \
	status=$$?; \
	chown --quiet "$$(id -u):$$(id -g)" assistant-bash.wasm assistant-bash.wasm.gz 2>/dev/null || true; \
	exit $$status

# R09:新检出按 docs/getting-started.md 完成构建、Bash/Python/Node 启动。
getting-started: build
	./assistant-sandbox --dir "$$(pwd)" -c 'printf getting-started-ok' | grep -q getting-started-ok
	go test ./deployment/native/ -run Example -count=1
	go test ./internal/guest/ -run TestPython312MinimalStartup -count=1 -timeout 1800s
	go test ./internal/guest/ -run TestNode22MinimalStartup -count=1 -timeout 1800s
	@echo "getting-started: 构建、Bash、Python、Node 与关闭通过"

compat-s3:
	go test ./internal/guest -run '^TestCompatibilityS3DevelopmentWorkflow$$' -count=1 -v -timeout 20m

compat-ltp:
	go test ./internal/guest -run '^TestCompatibilityExternalLTP$$' -count=1 -v -timeout 3h

compat-s4:
	go test ./internal/guest -run '^TestCompatibilityS4DevelopmentImageExternal$$' -count=1 -v -timeout 1h

# Q14:运行 CPython 上游测试。PYTHON_SUITE_TESTS="test_sys test_os" 指定模块，
# 或设为 all 运行完整测试集；PYTHON_SUITE_TIMEOUT=秒数 限制单模块时长。
compat-python:
	PYTHON_SUITE_TESTS="$(PYTHON_SUITE_TESTS)" go test ./internal/guest \
		-run '^TestPython312UpstreamSuite$$' -count=1 -v -timeout 3h
