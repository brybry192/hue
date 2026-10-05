BINARY  := hue
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/brybry192/hue/internal/cli.Version=$(VERSION)

.PHONY: build test race cover vet fmt install clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

race:
	go test ./... -race

cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

vet:
	go vet ./...

fmt:
	gofmt -l -w .

install:
	go install -ldflags "$(LDFLAGS)" .

clean:
	rm -f $(BINARY) coverage.out
