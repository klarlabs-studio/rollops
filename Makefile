# Rollops — developer tasks.
GO      ?= go
BINDIR  ?= bin
DISTDIR ?= dist
MODULE   = go.klarlabs.de/rollops
UI_DIR   = internal/ui/web
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS ?= -X '$(MODULE)/internal/version.Version=$(VERSION)' -X '$(MODULE)/internal/version.Commit=$(COMMIT)' -X '$(MODULE)/internal/version.Date=$(DATE)'
PLATFORMS ?= linux/amd64 linux/arm64 darwin/arm64

.PHONY: all build dist dist-check test race vet fmt fmt-check tidy proto ui-build ui-typecheck examples-check package-check release-check install-systemd run-daemon integration clean

all: fmt-check vet test build

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rollops  ./cmd/rollops
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rollopsd ./cmd/rollopsd

dist: ui-build
	rm -rf $(DISTDIR)
	mkdir -p $(DISTDIR)
	@set -e; \
	for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		name=rollops_$(VERSION)_$${os}_$${arch}; \
		stage=$$(mktemp -d); \
		mkdir -p "$$stage/$$name/bin" "$$stage/$$name/docs" "$$stage/$$name/deploy/systemd" "$$stage/$$name/scripts" "$$stage/$$name/examples"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o "$$stage/$$name/bin/rollops" ./cmd/rollops; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o "$$stage/$$name/bin/rollopsd" ./cmd/rollopsd; \
		cp README.md CHANGELOG.md "$$stage/$$name/"; \
		cp docs/first-run.md docs/deploy-systemd.md docs/security-rbac.md docs/oidc-auth.md docs/target-plugins.md docs/image-automation.md docs/studio-boundary.md docs/optional-integrations.md docs/multi-instance.md docs/database-rollback.md docs/risk-history.md docs/metric-analysis.md docs/release-checklist.md "$$stage/$$name/docs/"; \
		cp deploy/systemd/rollopsd.service deploy/systemd/rollopsd.env.example "$$stage/$$name/deploy/systemd/"; \
		cp scripts/install-systemd.sh "$$stage/$$name/scripts/"; \
		cp examples/*.yaml "$$stage/$$name/examples/"; \
		tar -C "$$stage" -czf "$(DISTDIR)/$$name.tar.gz" "$$name"; \
		rm -rf "$$stage"; \
	done
	cd $(DISTDIR) && shasum -a 256 *.tar.gz > checksums.txt

dist-check: dist
	test -s $(DISTDIR)/checksums.txt
	@count=$$(find $(DISTDIR) -name 'rollops_*.tar.gz' | wc -l | tr -d ' '); \
	want=$$(printf '%s\n' $(PLATFORMS) | wc -l | tr -d ' '); \
	if [ "$$count" != "$$want" ]; then echo "dist archive count $$count != $$want"; exit 1; fi
	cd $(DISTDIR) && shasum -a 256 -c checksums.txt

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	@git ls-files '*.go' | while IFS= read -r f; do [ ! -f "$$f" ] || printf '%s\0' "$$f"; done | xargs -0 gofmt -w

fmt-check:
	@unformatted=$$(git ls-files '*.go' | while IFS= read -r f; do [ ! -f "$$f" ] || printf '%s\0' "$$f"; done | xargs -0 gofmt -l); \
	if [ -n "$$unformatted" ]; then echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; fi

tidy:
	$(GO) mod tidy

ui-build:
	cd $(UI_DIR) && npm run build

ui-typecheck:
	cd $(UI_DIR) && npm run typecheck

examples-check:
	$(GO) test ./internal/config -run 'TestLoad_AllExamples|TestParse_Example'

package-check:
	bash -n scripts/install-systemd.sh
	shellcheck scripts/install-systemd.sh
	test -f deploy/systemd/rollopsd.service
	test -f deploy/systemd/rollopsd.env.example

release-check: ui-typecheck ui-build examples-check package-check dist-check all

install-systemd: build
	sudo scripts/install-systemd.sh --no-build

# Regenerate gRPC stubs (requires protoc, protoc-gen-go, protoc-gen-go-grpc).
proto:
	protoc --proto_path=proto \
		--go_out=. --go_opt=module=$(MODULE) \
		--go-grpc_out=. --go-grpc_opt=module=$(MODULE) \
		proto/rollops/v1/rollops.proto

# Run the daemon locally (HTTP :8080, gRPC :8090, UI behind basic auth).
run-daemon: build
	ROLLOPS_ADDR=:8080 ROLLOPS_GRPC_ADDR=:8090 \
	ROLLOPS_ADMIN_TOKEN=devtoken ROLLOPS_UI_USER=admin ROLLOPS_UI_PASSWORD=dev \
	./$(BINDIR)/rollopsd

# Live integration tests: dumb targets against real SSH + FTP servers in Docker.
# Requires Docker. Brings the env up, runs the tagged tests, tears it down.
# (If a kube context is reachable, the K8s target test runs too; else it skips.)
integration:
	./test/integration/run.sh

# Live Kubernetes target test against the current kube context (e.g. minikube).
# Start one first:  minikube start --apiserver-names=minikube
integration-k8s:
	$(GO) test -tags integration -count=1 -run TestKubernetesTarget_Live -v ./test/integration/...

clean:
	rm -rf $(BINDIR) $(DISTDIR)
