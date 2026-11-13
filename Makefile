# Perry. Run `make help` for the list.

GO      ?= go
BIN     ?= perry
CORPORA := testdata/cobra.git testdata/cobra-full.git testdata/grpc-go.git

.PHONY: help build test bench bench-setup bench-contention corpora clean

help: ## Show this help
	@echo "Perry"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "Benchmarks need corpora: make corpora (about 50 MB, cached)."

build: ## Build the server into ./perry
	$(GO) build -o $(BIN) ./cmd/perry

test: ## Run the test suite, skipping the slow benchmarks
	$(GO) test -short ./...

corpora: ## Clone the benchmark corpora into testdata/
	@sh scripts/corpora.sh

bench: corpora ## Run every benchmark (about 10 minutes)
	PERRY_BENCH=1 $(GO) test ./internal/bench -v -timeout 40m

bench-setup: corpora ## Just the setup-cost comparison: perry vs every git path
	PERRY_BENCH=1 $(GO) test ./internal/bench -run TestSetupPathsCompared -v -timeout 20m

bench-contention: corpora ## Just the same-ref contention comparison
	PERRY_BENCH=1 $(GO) test ./internal/bench -run TestSameRefContention -v -timeout 30m

clean: ## Remove build output (keeps the cached corpora)
	rm -f $(BIN) $(BIN).exe
