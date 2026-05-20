.PHONY: run build test fmt vet tidy

run:
	HEARTH_ENV=development go run ./cmd/server

build:
	CGO_ENABLED=0 go build -trimpath -o bin/hearth ./cmd/server

test:
	go test ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

tidy:
	go mod tidy
