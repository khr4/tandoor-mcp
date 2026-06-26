BINARY := tandoor-mcp

.PHONY: build test vet lint tidy run clean

build:
	go build -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run

tidy:
	go mod tidy

run:
	go run .

clean:
	rm -f $(BINARY)
