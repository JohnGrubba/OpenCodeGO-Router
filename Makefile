SHELL := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test vet run fmt docker up down logs clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/router ./cmd/router

test:
	go test ./...

vet:
	go vet ./...

run:
	go run ./cmd/router

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

docker:
	docker build --build-arg VERSION=$(VERSION) -t opencode-go-router:$(VERSION) .

up:
	docker compose up -d --build
	docker compose ps

down:
	docker compose down

logs:
	docker compose logs -f --tail=100 router

clean:
	rm -rf bin
