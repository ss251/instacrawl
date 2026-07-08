BINARY := instacrawl
VERSION ?= dev

.PHONY: build test test-race vet fmt tidy check clean

build:
	mkdir -p bin
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BINARY) ./cmd/instacrawl

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

check: vet test tidy
	git diff --exit-code -- go.mod go.sum

clean:
	rm -rf bin
