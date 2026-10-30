.PHONY: run dev build test web docker lint

## run: start the server (reads .env; offline demo mode if no keys are set)
run:
	go run ./cmd/mandate

## web: build the React UI into web/dist
web:
	npm --prefix web install --no-audit --no-fund
	npm --prefix web run build

## dev: run the Go API and the Vite dev server with hot reload
dev:
	@echo "API on :8080, UI on :5173 (proxied)"
	@(go run ./cmd/mandate &) && npm --prefix web run dev

## build: compile a static binary to ./bin/mandate
build: web
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/mandate ./cmd/mandate

## test: run the Go test suite
test:
	go vet ./...
	go test ./... -count=1

## docker: build and run the container
docker:
	docker build -t mandate .
	docker run --rm -p 8080:8080 --env-file .env mandate
