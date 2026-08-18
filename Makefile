GO_TOOLCHAIN := go1.26.6
GO := GOTOOLCHAIN=$(GO_TOOLCHAIN) go
GO_PACKAGES := ./cmd/... ./internal/...
STATICCHECK_VERSION := v0.7.0
GOVULNCHECK_VERSION := v1.7.0

.PHONY: bootstrap check go-check web-check deployment-check security-check dependency-security-check agent-binaries center-install-bundle image-center image-agent

bootstrap:
	$(GO) mod download
	cd web && npm ci --ignore-scripts

check: go-check web-check deployment-check

go-check:
	@VASTORA_FORMATTED_FILES="$$(gofmt -l cmd internal)"; \
	/bin/test -z "$$VASTORA_FORMATTED_FILES" || { printf 'Run gofmt on:\n%s\n' "$$VASTORA_FORMATTED_FILES"; exit 1; }
	$(GO) test -race $(GO_PACKAGES)
	$(GO) vet $(GO_PACKAGES)
	$(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) $(GO_PACKAGES)

web-check:
	cd web && npm run lint
	cd web && npm test
	cd web && npm run build

deployment-check:
	sh -n install.sh
	sh -n deploy/center/setup.sh
	sh -n scripts/package-center-install.sh
	sh -n scripts/test-center-install.sh
	./install.sh --help >/dev/null
	deploy/center/setup.sh --help >/dev/null
	scripts/package-center-install.sh --help >/dev/null
	scripts/test-center-install.sh

security-check:
	gitleaks detect --no-git --redact --source .
	$(MAKE) dependency-security-check

dependency-security-check:
	cd web && npm audit --audit-level=high
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) $(GO_PACKAGES)

agent-binaries:
	mkdir -p bin/agent-binaries
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w" -o bin/agent-binaries/linux-amd64 ./cmd/vastora

center-install-bundle:
	scripts/package-center-install.sh --version "$${VASTORA_VERSION:?set VASTORA_VERSION}" --image "$${VASTORA_CENTER_IMAGE:?set VASTORA_CENTER_IMAGE}" --output-dir dist

image-center:
	docker build --file Dockerfile.center --tag vastora-center:dev .

image-agent:
	docker build --file Dockerfile.agent --tag vastora-agent:dev .
