DATA  ?= ./.dev-library
ADDR  ?= 0.0.0.0:8080
ADMIN_USER ?= admin
ADMIN_PASS ?= devpass
BROWSER_WORKERS ?= 2
BROWSER_HOST ?= 127.0.0.1
BROWSER_LANE_A_PORT ?=
BROWSER_LANE_B_PORT ?=
BROWSER_PAGER_PORT ?=
BROWSER_RUN_ID ?= $(shell date +%s)-$(shell od -An -N4 -tx4 /dev/urandom | tr -d ' \n')
BROWSER_RUN_ID := $(BROWSER_RUN_ID)
BROWSER_LANE_A_DATA ?= .tmp-browser-lane-a-data-$(BROWSER_RUN_ID)
BROWSER_LANE_B_DATA ?= .tmp-browser-lane-b-data-$(BROWSER_RUN_ID)
BROWSER_PAGER_DATA ?= .tmp-browser-pager-data-$(BROWSER_RUN_ID)
BROWSER_FILLER ?= .tmp-browser-filler-$(BROWSER_RUN_ID)
BROWSER_AUTH_DIR ?= browser-test/.auth-$(BROWSER_RUN_ID)
POLKA_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BROWSER_LANE_A_DATA_ABS := $(abspath $(BROWSER_LANE_A_DATA))
BROWSER_LANE_B_DATA_ABS := $(abspath $(BROWSER_LANE_B_DATA))
BROWSER_PAGER_DATA_ABS := $(abspath $(BROWSER_PAGER_DATA))
BROWSER_FILLER_ABS := $(abspath $(BROWSER_FILLER))
BROWSER_AUTH_DIR_ABS := $(abspath $(BROWSER_AUTH_DIR))
GOFMT_FILES = $(shell go list -f '{{range .GoFiles}}{{$$.Dir}}/{{.}} {{end}}{{range .CgoFiles}}{{$$.Dir}}/{{.}} {{end}}{{range .TestGoFiles}}{{$$.Dir}}/{{.}} {{end}}{{range .XTestGoFiles}}{{$$.Dir}}/{{.}} {{end}}{{range .IgnoredGoFiles}}{{$$.Dir}}/{{.}} {{end}}' ./...)
PUBLIC_SEED_INPUT ?= browser-test/fixtures
LOCAL_SEED_INPUT ?= local/corpus/dev-seed
SEED_INPUT ?= $(if $(wildcard $(LOCAL_SEED_INPUT)),$(LOCAL_SEED_INPUT),$(PUBLIC_SEED_INPUT))

.PHONY: help test build serve seed reseed browser-test pdfium-wasm-verify node-deps frontend

help:
	@echo "polka:"
	@echo "  make test          autoformat, then run checks (vet, biome, tsc, frontend/unit build, go test)"
	@echo "  make build         bundle the frontend, then build the binary"
	@echo "  make pdfium-wasm-verify"
	@echo "                    verify the tailored PDFium module and dependency pin"
	@echo "  make seed          import the dev seed into the dev library (idempotent)"
	@echo "  make reseed        wipe the dev library and rebuild it from scratch"
	@echo "  make serve         build, auto-seed on first run, and serve the dev library"
	@echo "                     override books with SEED_INPUT=/path/to/books"
	@echo "  make browser-test  compile app, start isolated servers, and run Playwright UI tests"

node-deps: node_modules/.package-lock.json

node_modules/.package-lock.json: package.json package-lock.json
	npm ci

frontend: node-deps
	npm run build

test: node-deps pdfium-wasm-verify
	@gofmt -w $(GOFMT_FILES)
	npm run lint:fix
	npm run typecheck
	npm run test:unit
	npm run test:browser-list
	npm run build
	go vet ./...
	go test ./...

# Derivation instructions and provenance live in internal/pdfcover/README.md.
pdfium-wasm-verify:
	go run ./internal/pdfcover/wasmtool verify

build: frontend
	CGO_ENABLED=0 go build -ldflags "-X github.com/levmv/polka/internal/version.Version=$(POLKA_VERSION)" -o polka .

# Three isolated servers keep stateful browser lanes independent; pagination
# uses a separate filler-only library (>50 books).
browser-test: build
	@set -eu; \
	LANE_A_PID=; \
	LANE_B_PID=; \
	PAGER_PID=; \
	free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'; }; \
	LANE_A_PORT='$(BROWSER_LANE_A_PORT)'; \
	LANE_B_PORT='$(BROWSER_LANE_B_PORT)'; \
	PAGER_PORT='$(BROWSER_PAGER_PORT)'; \
	if [ -z "$$LANE_A_PORT" ]; then LANE_A_PORT=$$(free_port); fi; \
	if [ -z "$$LANE_B_PORT" ]; then LANE_B_PORT=$$(free_port); fi; \
	if [ -z "$$PAGER_PORT" ]; then PAGER_PORT=$$(free_port); fi; \
	cleanup() { \
		if [ -n "$$LANE_A_PID" ]; then kill "$$LANE_A_PID" 2>/dev/null || true; fi; \
		if [ -n "$$LANE_B_PID" ]; then kill "$$LANE_B_PID" 2>/dev/null || true; fi; \
		if [ -n "$$PAGER_PID" ]; then kill "$$PAGER_PID" 2>/dev/null || true; fi; \
		if [ -n "$$LANE_A_PID" ]; then wait "$$LANE_A_PID" 2>/dev/null || true; fi; \
		if [ -n "$$LANE_B_PID" ]; then wait "$$LANE_B_PID" 2>/dev/null || true; fi; \
		if [ -n "$$PAGER_PID" ]; then wait "$$PAGER_PID" 2>/dev/null || true; fi; \
		rm -rf $(BROWSER_LANE_A_DATA_ABS) $(BROWSER_LANE_B_DATA_ABS) $(BROWSER_PAGER_DATA_ABS) $(BROWSER_FILLER_ABS) $(BROWSER_AUTH_DIR_ABS); \
	}; \
	trap cleanup EXIT; \
	trap 'exit 130' INT TERM; \
	echo "Running browser tests..."; \
	mkdir -p $(CURDIR)/browser-test/screenshots; \
	find $(CURDIR)/browser-test/screenshots -mindepth 1 -delete; \
	rm -rf $(BROWSER_LANE_A_DATA_ABS) $(BROWSER_LANE_B_DATA_ABS) $(BROWSER_PAGER_DATA_ABS) $(BROWSER_FILLER_ABS) $(BROWSER_AUTH_DIR_ABS); \
	./polka import browser-test/fixtures --data $(BROWSER_LANE_A_DATA_ABS); \
	cp -a $(BROWSER_LANE_A_DATA_ABS) $(BROWSER_LANE_B_DATA_ABS); \
	python3 browser-test/fixtures/generate.py --filler 55 $(BROWSER_FILLER_ABS); \
	./polka import $(BROWSER_FILLER_ABS) --data $(BROWSER_PAGER_DATA_ABS); \
	rm -rf $(BROWSER_FILLER_ABS); \
	./polka serve --addr $(BROWSER_HOST):$$LANE_A_PORT --data $(BROWSER_LANE_A_DATA_ABS) --admin-user admin --admin-password devpass >/dev/null 2>&1 & \
	LANE_A_PID=$$!; \
	./polka serve --addr $(BROWSER_HOST):$$LANE_B_PORT --data $(BROWSER_LANE_B_DATA_ABS) --admin-user admin --admin-password devpass >/dev/null 2>&1 & \
	LANE_B_PID=$$!; \
	./polka serve --addr $(BROWSER_HOST):$$PAGER_PORT --data $(BROWSER_PAGER_DATA_ABS) --admin-user admin --admin-password devpass >/dev/null 2>&1 & \
	PAGER_PID=$$!; \
	python3 browser-test/wait-for-servers.py http://$(BROWSER_HOST):$$LANE_A_PORT/login http://$(BROWSER_HOST):$$LANE_B_PORT/login http://$(BROWSER_HOST):$$PAGER_PORT/login; \
	cd browser-test && PLAYWRIGHT_BROWSERS_PATH=~/.cache/ms-playwright PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 POLKA_LANE_A_BASE_URL=http://$(BROWSER_HOST):$$LANE_A_PORT POLKA_LANE_B_BASE_URL=http://$(BROWSER_HOST):$$LANE_B_PORT POLKA_PAGER_BASE_URL=http://$(BROWSER_HOST):$$PAGER_PORT POLKA_AUTH_STATE_DIR=$(BROWSER_AUTH_DIR_ABS) POLKA_BROWSER_WORKERS=$(BROWSER_WORKERS) npx playwright test $(PWARGS)

seed: build
	./polka import "$(SEED_INPUT)" --data "$(DATA)"

# Destructive; refuse paths that could remove the workspace or a home directory.
reseed:
	@data_abs='$(abspath $(DATA))'; \
	case "$$data_abs" in \
		""|/|"$$HOME") echo "refusing to delete unsafe DATA path: $$data_abs" >&2; exit 1 ;; \
	esac; \
	case '$(CURDIR)/' in \
		"$$data_abs/"*) echo "refusing to delete DATA path containing the workspace: $$data_abs" >&2; exit 1 ;; \
	esac; \
	rm -rf -- "$$data_abs"
	@$(MAKE) seed

serve: build
	@if [ ! -f "$(DATA)/library.db" ]; then \
		echo "Seeding dev library from $(SEED_INPUT)"; \
		./polka import "$(SEED_INPUT)" --data "$(DATA)"; \
	fi
	./polka serve --data $(DATA) --addr $(ADDR) --admin-user "$(ADMIN_USER)" --admin-password "$(ADMIN_PASS)"
