SHELL := /bin/bash

CANVAS_SOURCE_URL ?= https://github.com/dolorous01/sub2api/tree/6b390391c7d418567f2342c0b99fa0d558eaaece
CANVAS_BUILD_ID ?= $(shell tr -d '[:space:]' < VERSION)

.PHONY: fmt fmt-check lint test test-deploy build secret-scan check-canvas-license clean

fmt:
	cd backend && gofmt -w $$(find . -name '*.go' -type f)

fmt-check:
	@test -z "$$(gofmt -l backend)" || { gofmt -l backend; exit 1; }

lint: fmt-check
	cd backend && go vet ./...
	cd frontend && pnpm run typecheck

test:
	cd backend && go test ./...
	cd frontend && pnpm run test

test-deploy:
	bash -n deploy/*.sh deploy/tests/*.sh
	python3 -m unittest discover -s deploy/proxy -p 'test_*.py' -v
	python3 -m unittest discover -s deploy/operator -p 'test_*.py' -v
	python3 -m unittest discover -s deploy/reconcile -p 'test_*.py' -v
	./deploy/tests/test-sub2api-guard.sh
	./deploy/tests/test-reconcile-shell.sh

build:
	mkdir -p bin
	cd backend && go build -trimpath -o ../bin/canvas-api ./cmd/canvas-api
	cd backend && go build -trimpath -o ../bin/canvas-worker ./cmd/canvas-worker
	cd backend && go build -trimpath -o ../bin/canvas-migrate ./cmd/canvas-migrate
	cd backend && go build -trimpath -o ../bin/canvas-contract ./cmd/canvas-contract
	cd backend && go build -trimpath -o ../bin/canvas-healthcheck ./cmd/canvas-healthcheck
	cd frontend && VITE_CANVAS_SOURCE_URL="$(CANVAS_SOURCE_URL)" VITE_CANVAS_BUILD_ID="$(CANVAS_BUILD_ID)" pnpm run build

secret-scan:
	@! rg -n --hidden \
		-g '!.git/**' -g '!node_modules/**' -g '!docs/**' -g '!Makefile' \
		'(BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY|Authorization:[[:space:]]*Bearer[[:space:]]+[A-Za-z0-9._-]{16,}|sk-[A-Za-z0-9_-]{16,})' .

check-canvas-license:
	VITE_CANVAS_SOURCE_URL="$(CANVAS_SOURCE_URL)" node frontend/scripts/check-license.mjs
	node frontend/scripts/check-upstream.mjs

clean:
	rm -rf bin dist frontend/coverage
