.DEFAULT_GOAL := check
export GOTOOLCHAIN := go$(strip $(shell cat .go-version))

# Discover checked-in projects, including new plugins outside link/.
GO_MODULES := $(patsubst %/go.mod,%,$(wildcard $(shell git ls-files '*/go.mod')))
# A plugin with a missing npm lockfile must still reach npm ci.
TS_PROJECT_FILES := $(wildcard $(shell git ls-files '*/package-lock.json' '*/tsdown.config.*'))
TS_PACKAGES := $(sort $(patsubst %/,%,$(dir $(TS_PROJECT_FILES))))

.PHONY: setup fmt fmt-check check test go-check go-vet go-test ts-setup typecheck ts-test list-go list-ts
.PHONY: integration integration-tcp integration-grpc integration-webrtc integration-peer-group

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

integration: integration-tcp integration-grpc integration-webrtc $(if $(MDNS_TEST_INTERFACE),integration-peer-group)

integration-peer-group:
	@test -n "$$MDNS_TEST_INTERFACE" || { echo "Set MDNS_TEST_INTERFACE to the multicast interface for the ring test" >&2; exit 1; }
	# Build before starting the timed three-host test on a cold runner.
	cd link/tcp && go build -mod=readonly -o /dev/null ./cmd/link-tcp
	cd discovery/mdns && go build -mod=readonly -o /dev/null ./cmd/discover-mdns
	cd group/peer && go build -mod=readonly -o /dev/null ./cmd/peer-group
	node --test --test-reporter=./scripts/integration-reporter.mjs group/peer/test/ring.test.mjs

integration-grpc:
	@host=$$(cd link/tcp/node && node -p "require.resolve('@arcships/rutis-host-'+process.platform+'-'+process.arch+'/bin/rutis-host')") && \
		RUTIS_HOST="$$host" sh scripts/in-go-module.sh link/grpc go test -mod=readonly -race -count=1 ./...

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
