GO ?= go
ROOT := $(abspath ../..)
OUT ?= .generated
TET_ROOT ?= $(CURDIR)/.local/tet3.8
SANDBOX_CLI ?= $(ROOT)/assistant-sandbox


WASM := $(ROOT)/assistant-bash.wasm.gz

$(WASM):
	@$(MAKE) --no-print-directory -C "$(ROOT)" guest

.PHONY: check gate test setup tet-smoke journal clean
check: $(WASM)
	@$(GO) run . -root "$(ROOT)" -out "$(OUT)/current"
gate: $(WASM)
	@$(GO) run . -root "$(ROOT)" -out "$(OUT)/current" -strict
test:
	@$(GO) test -race -count=1 -cover .
setup:
	@./setup-tet.sh
tet-smoke:
	@$(MAKE) --no-print-directory -C "$(ROOT)" build
	@mkdir -p "$(OUT)/tet-results"
	@TET_ROOT="$(TET_ROOT)" TET_SUITE_ROOT="$(CURDIR)" \
		SANDBOX_CLI="$(SANDBOX_CLI)" WASM_MODULE="$(WASM)" POSIX_OUT="$(abspath $(OUT))" \
		"$(TET_ROOT)/bin/tcc" -e -t 30 -i "$(abspath $(OUT))/tet-results" \
		-j "$(abspath $(OUT))/tet-smoke.journal" tet all
	@$(GO) run . -journal "$(OUT)/tet-smoke.journal" -suite assistant-posix-smoke \
		-suite-version development -target assistant-wasi -out "$(OUT)/tet-smoke-report"
journal:
	@$(GO) run . -journal "$(JOURNAL)" -suite "$(SUITE)" -suite-version "$(SUITE_VERSION)" \
		-target "$(TARGET)" -out "$(OUT)/journal"
clean:
	@rm -rf -- .generated

.PHONY: doctor
doctor:
	@./doctor.sh

.PHONY: sources upstream upstream-gate
sources:
	@$(GO) run . -fetch
upstream:
	@$(GO) run . -upstream -filter "$(FILTER)" -out "$(OUT)/upstream"
upstream-gate:
	@$(GO) run . -upstream -filter "$(FILTER)" -out "$(OUT)/upstream" -strict
