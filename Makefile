BINARY  := ey
PKG     := ./cmd/ey
BIN     := bin
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

LDFLAGS := -s -w -X ey/internal/config.BuildVersion=$(VERSION)

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

IMAGE := ey
TAG   := $(VERSION)

.PHONY: build run test vet fmt lint dist docker docker-run clean

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/$(BINARY) $(PKG)

run:
	go run $(PKG) $(ARGS)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

lint: fmt vet test

dist:
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		out=$(BIN)/$(BINARY)-$$os-$$arch; \
		if [ "$$os" = "windows" ]; then out=$$out.exe; fi; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
			go build -trimpath -ldflags '$(LDFLAGS)' -o $$out $(PKG) || exit 1; \
	done

clean:
	rm -rf $(BIN)

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) .

docker-run:
	docker run --rm --pid=host -v /:/host:ro $(IMAGE):$(TAG) -path /host -json
