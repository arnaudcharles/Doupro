BINARY  := doupro
IMAGE   := sharlihe/doupro
VERSION ?= dev

.PHONY: build run test test-integration lint docker-build docker-run clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) ./cmd/doupro

run: build
	./bin/$(BINARY) serve

test:
	go test ./...

test-integration:
	DOUPRO_DOCKER_INTEGRATION=1 go test -tags=integration -count=1 ./internal/updater

lint:
	go vet ./...
	golangci-lint run

docker-build:
	docker build -t $(IMAGE):$(VERSION) .

docker-run: docker-build
	docker compose up

clean:
	rm -rf bin/
