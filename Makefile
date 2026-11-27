.DEFAULT_GOAL := check
export GOTOOLCHAIN := go$(strip $(shell cat .go-version))

# Discover checked-in projects, including new plugins outside link/.
GO_MODULES := $(patsubst %/go.mod,%,$(wildcard $(shell git ls-files '*/go.mod')))
# A plugin with a missing npm lockfile must still reach npm ci.
TS_PROJECT_FILES := $(wildcard $(shell git ls-files '*/package-lock.json' '*/tsdown.config.*'))
TS_PACKAGES := $(sort $(patsubst %/,%,$(dir $(TS_PROJECT_FILES))))

.PHONY: setup fmt fmt-check check test go-check go-vet go-test ts-setup typecheck ts-test list-go list-ts
.PHONY: integration integration-tcp integration-webrtc

setup: ts-setup
	npm ci

ts-setup:
	@set -e; for pkg in $(TS_PACKAGES); do npm ci --prefix "$$pkg"; done

fmt fmt-check:
	node scripts/format.mjs $@

check: fmt-check go-vet typecheck test

test: go-test ts-test

go-check: go-vet go-test

go-vet:
	@set -e; for mod in $(GO_MODULES); do sh scripts/in-go-module.sh "$$mod" go vet -mod=readonly ./...; done

go-test:
	@set -e; for mod in $(GO_MODULES); do sh scripts/in-go-module.sh "$$mod" go test -mod=readonly -short ./...; done

integration: integration-tcp integration-webrtc

integration-tcp:
	# Warm the Go build cache before the timed host tests start.
	sh scripts/in-go-module.sh link/tcp go build -mod=readonly ./cmd/link-tcp ./test/fixtures/mounter
	sh scripts/in-go-module.sh link/tcp npm --prefix node run test:integration

integration-webrtc:
	sh scripts/in-go-module.sh link/webrtc npm --prefix web run test:integration

typecheck:
	@set -e; for pkg in $(TS_PACKAGES); do echo "Typecheck $$pkg"; npm run typecheck --prefix "$$pkg"; done

ts-test:
	@set -e; for pkg in $(TS_PACKAGES); do echo "Test $$pkg"; npm run test:unit --prefix "$$pkg"; done

list-go:
	@printf '%s\n' $(GO_MODULES)

list-ts:
	@printf '%s\n' $(TS_PACKAGES)
