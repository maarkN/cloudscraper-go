.PHONY: build test test-net bench lint vet fmt fmt-check tidy fingerprint clean install

BIN := bin

build: ## Build both binaries into ./bin
	@mkdir -p $(BIN)
	go build -o $(BIN)/cloudscraper ./cmd/cloudscraper
	go build -o $(BIN)/cloudscraper-server ./cmd/server

test: ## Offline, deterministic test suite (race)
	go test -race -short ./...

test-net: ## Full suite including network fingerprint tests
	go test -race ./...

bench: ## Network benchmark
	go test -bench . -run '^$$' ./internal/transport

lint: ## Run golangci-lint (install: https://golangci-lint.run)
	golangci-lint run ./...

vet:
	go vet ./...

fmt: ## Format the code
	gofmt -s -w .

fmt-check: ## Fail if code is not gofmt-clean
	@out=$$(gofmt -s -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy:
	go mod tidy

fingerprint: build ## Build and probe tls.peet.ws with the chrome profile
	./$(BIN)/cloudscraper fingerprint --profile chrome

install:
	go install ./cmd/cloudscraper

clean:
	rm -rf $(BIN)
