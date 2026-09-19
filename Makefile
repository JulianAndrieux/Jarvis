.PHONY: test test-integration build fmt vet

test:
	go test ./...

# Nécessite pdftotext (poppler-utils) sur le PATH.
test-integration:
	go test ./... -tags=integration

build:
	go build -o bin/jarvis ./cmd/jarvis

fmt:
	gofmt -l .

vet:
	go vet ./...
