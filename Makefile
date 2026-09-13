DATA  ?= ./.dev-library
ADDR  ?= 0.0.0.0:8080
ADMIN_USER ?= admin
ADMIN_PASS ?= devpass
POLKA_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
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
	CGO_ENABLED=0 go vet -tags nodynamic ./...
	CGO_ENABLED=0 go test -tags nodynamic ./...

# Derivation instructions and provenance live in internal/pdfcover/README.md.
pdfium-wasm-verify:
	go run ./internal/pdfcover/wasmtool verify

build: frontend
	CGO_ENABLED=0 go build -trimpath -tags nodynamic -ldflags "-s -w -X github.com/levmv/polka/internal/version.Version=$(POLKA_VERSION)" -o polka .

browser-test: build
	npm run e2e -- $(PWARGS)

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
