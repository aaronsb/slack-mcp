# If you update this file, please follow
# https://suva.sh/posts/well-documented-makefiles

.DEFAULT_GOAL := help

TAG ?=
PACKAGE = $(shell go list -m)
GIT_COMMIT_HASH = $(shell git rev-parse HEAD)
GIT_VERSION = $(shell git describe --tags --always --dirty)
BUILD_TIME = $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
BINARY_NAME = slack-mcp
LD_FLAGS = -s -w \
	-X '$(PACKAGE)/pkg/version.CommitHash=$(GIT_COMMIT_HASH)' \
	-X '$(PACKAGE)/pkg/version.Version=$(GIT_VERSION)' \
	-X '$(PACKAGE)/pkg/version.BuildTime=$(BUILD_TIME)' \
	-X '$(PACKAGE)/pkg/version.BinaryName=$(BINARY_NAME)'
COMMON_BUILD_ARGS = -ldflags "$(LD_FLAGS)"

NPM_VERSION = $(shell git describe --tags --always | sed 's/^v//' | cut -d- -f1)
NPM_PKG_PREFIX = slack-mcp-server
OSES = darwin linux windows
ARCHS = amd64 arm64

CLEAN_TARGETS :=
CLEAN_TARGETS += '$(BINARY_NAME)'
CLEAN_TARGETS += $(foreach os,$(OSES),$(foreach arch,$(ARCHS),./build/$(BINARY_NAME)-$(os)-$(arch)$(if $(findstring windows,$(os)),.exe,)))
CLEAN_TARGETS += $(foreach os,$(OSES),$(foreach arch,$(ARCHS),./npm/$(NPM_PKG_PREFIX)-$(os)-$(arch)/bin/))
CLEAN_TARGETS += ./npm/$(NPM_PKG_PREFIX)/.npmrc ./npm/$(NPM_PKG_PREFIX)/LICENSE ./npm/$(NPM_PKG_PREFIX)/README.md
CLEAN_TARGETS += $(foreach os,$(OSES),$(foreach arch,$(ARCHS),./npm/$(NPM_PKG_PREFIX)-$(os)-$(arch)/.npmrc))
CLEAN_TARGETS += mcpb/bin $(BINARY_NAME)-*.mcpb

# The help will print out all targets with their descriptions organized bellow their categories. The categories are represented by `##@` and the target descriptions by `##`.
# The awk commands is responsible to read the entire set of makefiles included in this invocation, looking for lines of the file as xyz: ## something, and then pretty-format the target and help. Then, if there's a line with ##@ something, that gets pretty-printed as a category.
# More info over the usage of ANSI control characters for terminal formatting: https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info over awk command: http://linuxcommand.org/lc3_adv_awk.php
#
# Notice that we have a little modification on the awk command to support slash in the recipe name:
# origin: /^[a-zA-Z_0-9-]+:.*?##/
# modified /^[a-zA-Z_0-9\/\.-]+:.*?##/
.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9\/\.-]+:.*?##/ { printf "  \033[36m%-21s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: clean
clean: ## Clean up all build artifacts
	rm -rf $(CLEAN_TARGETS)

.PHONY: build
build: clean tidy format ## Build the project
	go build $(COMMON_BUILD_ARGS) -o ./build/$(BINARY_NAME) ./cmd/slack-mcp


.PHONY: build-all-platforms
build-all-platforms: clean tidy format ## Build the project for all platforms
	$(foreach os,$(OSES),$(foreach arch,$(ARCHS), \
		GOOS=$(os) GOARCH=$(arch) go build $(COMMON_BUILD_ARGS) -o ./build/$(BINARY_NAME)-$(os)-$(arch)$(if $(findstring windows,$(os)),.exe,) ./cmd/slack-mcp; \
	))

.PHONY: npm-copy-binaries
npm-copy-binaries: build-all-platforms ## Copy the binaries to each npm package
	$(foreach os,$(OSES),$(foreach arch,$(ARCHS), \
		EXECUTABLE=$(BINARY_NAME)-$(os)-$(arch)$(if $(findstring windows,$(os)),.exe,); \
		DIRNAME=$(NPM_PKG_PREFIX)-$(os)-$(arch); \
		mkdir -p ./npm/$$DIRNAME/bin; \
		cp ./build/$$EXECUTABLE ./npm/$$DIRNAME/bin/; \
	))

.PHONY: npm-set-version
npm-set-version: ## Set version in all npm package.json files
	@if [ -z "$(NPM_VERSION)" ]; then echo "NPM_VERSION not set (tag the repo first)"; exit 1; fi
	$(foreach os,$(OSES),$(foreach arch,$(ARCHS), \
		DIRNAME="$(NPM_PKG_PREFIX)-$(os)-$(arch)"; \
		jq '.version = "$(NPM_VERSION)"' npm/$$DIRNAME/package.json > npm/$$DIRNAME/tmp.json && mv npm/$$DIRNAME/tmp.json npm/$$DIRNAME/package.json; \
	))
	jq '.version = "$(NPM_VERSION)"' npm/$(NPM_PKG_PREFIX)/package.json > npm/$(NPM_PKG_PREFIX)/tmp.json && mv npm/$(NPM_PKG_PREFIX)/tmp.json npm/$(NPM_PKG_PREFIX)/package.json
	jq '.optionalDependencies |= with_entries(.value = "$(NPM_VERSION)")' npm/$(NPM_PKG_PREFIX)/package.json > npm/$(NPM_PKG_PREFIX)/tmp.json && mv npm/$(NPM_PKG_PREFIX)/tmp.json npm/$(NPM_PKG_PREFIX)/package.json

.PHONY: npm-publish
npm-publish: npm-copy-binaries npm-set-version ## Publish all npm packages (requires NPM_TOKEN or npm login)
	cp README.md LICENSE ./npm/$(NPM_PKG_PREFIX)/
	$(foreach os,$(OSES),$(foreach arch,$(ARCHS), \
		DIRNAME="$(NPM_PKG_PREFIX)-$(os)-$(arch)"; \
		cd npm/$$DIRNAME && npm publish --access public $(NPM_PUBLISH_FLAGS) && cd ../..; \
	))
	cd npm/$(NPM_PKG_PREFIX) && npm publish --access public $(NPM_PUBLISH_FLAGS)

.PHONY: test
test: ## Run the tests
	go test -count=1 -v ./...

.PHONY: format
format: ## Format the code
	go fmt ./...

.PHONY: tidy
tidy: ## Tidy up the go modules
	go mod tidy

MCPB_PLATFORMS = darwin-arm64 darwin-x64 linux-arm64 linux-x64 windows-x64

# Map mcpb platform names to our build artifact names
# mcpb: darwin-arm64 → build: slack-mcp-darwin-arm64
mcpb_to_binary = $(BINARY_NAME)-$(subst x64,amd64,$(subst windows,windows,$(1)))$(if $(findstring windows,$(1)),.exe,)

.PHONY: mcpb
mcpb: ## Build .mcpb for a single platform. Usage: make mcpb PLATFORM=linux-x64
	@if [ -z "$(PLATFORM)" ]; then echo "Usage: make mcpb PLATFORM=linux-x64"; exit 1; fi
	@echo "Building mcpb for $(PLATFORM)..."
	rm -rf mcpb/bin
	mkdir -p mcpb/bin
	cp ./build/$(call mcpb_to_binary,$(PLATFORM)) mcpb/bin/$(BINARY_NAME)$(if $(findstring windows,$(PLATFORM)),.exe,)
	mcpb pack mcpb $(BINARY_NAME)-$(PLATFORM).mcpb
	@echo "Built: $(BINARY_NAME)-$(PLATFORM).mcpb ($$(du -h $(BINARY_NAME)-$(PLATFORM).mcpb | cut -f1))"

.PHONY: mcpb-all
mcpb-all: build-all-platforms ## Build .mcpb for all platforms
	@for plat in $(MCPB_PLATFORMS); do \
		echo ""; \
		echo "=== $$plat ==="; \
		$(MAKE) mcpb PLATFORM=$$plat; \
	done

.PHONY: release
release: ## Create release tag. Usage: make tag TAG=v1.2.3
	@if [ -z "$(TAG)" ]; then \
	  echo "Usage: make tag TAG=vX.Y.Z"; exit 1; \
	fi
	git tag -a "$(TAG)" -m "Release $(TAG)"
	git push origin "$(TAG)"
