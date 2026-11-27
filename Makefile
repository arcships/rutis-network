.DEFAULT_GOAL := check

# Discover checked-in projects, including new plugins outside link/.
GO_MODULES := $(patsubst %/go.mod,%,$(wildcard $(shell git ls-files '*/go.mod')))
TS_PACKAGES := $(patsubst %/package-lock.json,%,$(wildcard $(shell git ls-files '*/package-lock.json')))

.PHONY: setup fmt fmt-check check test go-check go-vet go-test ts-setup typecheck ts-test list-go list-ts

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
	@set -e; for mod in $(GO_MODULES); do sh scripts/module-go.sh "$$mod" vet -mod=readonly ./...; done

go-test:
	@set -e; for mod in $(GO_MODULES); do sh scripts/module-go.sh "$$mod" test -mod=readonly -short ./...; done

typecheck:
	@set -e; for pkg in $(TS_PACKAGES); do echo "Typecheck $$pkg"; npm run typecheck --prefix "$$pkg"; done

ts-test:
	@set -e; for pkg in $(TS_PACKAGES); do echo "Test $$pkg"; npm run test:unit --prefix "$$pkg"; done

list-go:
	@printf '%s\n' $(GO_MODULES)

list-ts:
	@printf '%s\n' $(TS_PACKAGES)
