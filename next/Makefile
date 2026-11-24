# Short names for the checks AGENTS.md ("Build and test") lists per folder. Run from next/.
# Each target runs the same commands as AGENTS.md and CI; those stay the reference.

.DEFAULT_GOAL := help
.PHONY: help panel dev check test test-short ui ui-browser app app-rust tools all hooks

PORT ?= 8330

help: ## List the targets
	@awk -F':.*## ' '/^[a-z-]+:.*## /{printf "  make %-11s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

panel: ## Build the web panel core embeds (go generate ./internal/panel)
	go generate ./internal/panel

dev: ## Watch next/ui, rebuild the panel and serve it from a demo core (PORT=8330)
	@cd ui && bun install --frozen-lockfile --silent && exec bun dev.ts --port $(PORT)

check: ## Go build, vet and gofmt (fails if gofmt lists a file)
	go build ./...
	go vet ./...
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "These files need gofmt (fix: gofmt -w .):"; echo "$$unformatted"; exit 1; \
	fi

test: ## Whole Go suite with -race, including e2e, corpus and conformance
	CGO_ENABLED=1 go test -race -count=1 ./...

test-short: ## Go suite with -race and -short, without e2e, corpus and conformance
	CGO_ENABLED=1 go test -race -count=1 -short $$(go list ./... | grep -Ev '/(e2e|corpus|conformance)(/|$$)')

ui: ## UI kit: install, type check, Biome, unit tests
	cd ui && bun install --frozen-lockfile && bun run check && bun run lint && bun run test

ui-browser: ## UI kit browser test (headless Chromium)
	cd ui && bun run test:browser

app: ## Desktop app, bun side: install (the kit's packages too), check, lint, test, build
	cd ui && bun install --frozen-lockfile
	cd app && bun install --frozen-lockfile && bun run check && bun run lint && bun run test && bun run build

app-rust: ## Desktop app, Rust side: cargo fmt --check, cargo test
	cd app/src-tauri && cargo fmt --check && cargo test --locked

tools: ## app-tools: cargo fmt --check, clippy, test
	cd app-tools && cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked

all: check ui app ## check, ui and app (not the long suites)

hooks: ## Turn on the pre-commit hook in .githooks (formatting of staged files)
	git config core.hooksPath .githooks
	@echo "Pre-commit hook on for this clone and its worktrees. Turn it off: git config --unset core.hooksPath"
