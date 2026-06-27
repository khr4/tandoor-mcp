BINARY := tandoor-mcp
GIT_SECRETS_PROVIDER := $(CURDIR)/.githooks/git-secrets-provider
CACHE_DIR ?= $(CURDIR)/.cache
GOCACHE ?= $(CACHE_DIR)/go-build
GOLANGCI_LINT_CACHE ?= $(CACHE_DIR)/golangci-lint
export GOCACHE
export GOLANGCI_LINT_CACHE

.PHONY: build test vet lint tidy run clean install-hooks secret-scan verify

build:
	go build -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

verify: vet test lint secret-scan

install-hooks:
	@command -v git-secrets >/dev/null 2>&1 || { echo "git-secrets is required: install https://github.com/awslabs/git-secrets"; exit 1; }
	git config core.hooksPath .githooks
	@git config --get-all secrets.providers | grep -F -x -- "$(GIT_SECRETS_PROVIDER)" >/dev/null 2>&1 || git secrets --add-provider -- "$(GIT_SECRETS_PROVIDER)"

secret-scan:
	@command -v git-secrets >/dev/null 2>&1 || { echo "git-secrets is required: install https://github.com/awslabs/git-secrets"; exit 1; }
	@git config --get-all secrets.providers | grep -F -x -- "$(GIT_SECRETS_PROVIDER)" >/dev/null 2>&1 || git secrets --add-provider -- "$(GIT_SECRETS_PROVIDER)"
	git secrets --scan

tidy:
	go mod tidy

run:
	go run .

clean:
	rm -f $(BINARY)
