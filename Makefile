.PHONY: frontend build run test dev-web
frontend:
	npm --prefix web run build
build: frontend
	go build -o bin/undercover ./cmd/undercover
run: build
	./bin/undercover
test:
	go test -race ./...
	npm --prefix web run typecheck
	npm --prefix web test
dev-web:
	npm --prefix web run dev
