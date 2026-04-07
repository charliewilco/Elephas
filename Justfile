set shell := ["zsh", "-cu"]

default:
    @just --list

fmt:
    gofmt -w .

fmt-check:
    unformatted="$(find . -name '*.go' -type f -print | xargs gofmt -l)"; \
    if [[ -n "$unformatted" ]]; then \
      echo "Unformatted Go files:"; \
      echo "$unformatted"; \
      exit 1; \
    fi

tidy:
    go mod tidy

build:
    go build ./...

test:
    go test ./...

vet:
    go vet ./...

lint:
    "$(go env GOPATH)/bin/staticcheck" ./...

check: fmt-check build test vet lint

ci: tidy check

run:
    go run ./cmd/elephas
