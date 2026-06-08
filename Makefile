# Rolloffs — developer tasks.
GO      ?= go
BINDIR  ?= bin
MODULE   = go.klarlabs.de/rolloffs

.PHONY: all build test race vet fmt fmt-check tidy proto run-daemon integration clean

all: fmt-check vet test build

build:
	$(GO) build -o $(BINDIR)/rolloffs  ./cmd/rolloffs
	$(GO) build -o $(BINDIR)/rolloffsd ./cmd/rolloffsd

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w $(shell git ls-files '*.go')

fmt-check:
	@unformatted=$$(gofmt -l $(shell git ls-files '*.go')); \
	if [ -n "$$unformatted" ]; then echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; fi

tidy:
	$(GO) mod tidy

# Regenerate gRPC stubs (requires protoc, protoc-gen-go, protoc-gen-go-grpc).
proto:
	protoc --proto_path=proto \
		--go_out=. --go_opt=module=$(MODULE) \
		--go-grpc_out=. --go-grpc_opt=module=$(MODULE) \
		proto/rolloffs/v1/rolloffs.proto

# Run the daemon locally (HTTP :8080, gRPC :8090, UI behind basic auth).
run-daemon: build
	ROLLOFFS_ADDR=:8080 ROLLOFFS_GRPC_ADDR=:8090 \
	ROLLOFFS_ADMIN_TOKEN=devtoken ROLLOFFS_UI_USER=admin ROLLOFFS_UI_PASSWORD=dev \
	./$(BINDIR)/rolloffsd

# Live integration tests: dumb targets against real SSH + FTP servers in Docker.
# Requires Docker. Brings the env up, runs the tagged tests, tears it down.
integration:
	./test/integration/run.sh

clean:
	rm -rf $(BINDIR)
