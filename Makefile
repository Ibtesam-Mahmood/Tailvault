VERSION := $(shell cat VERSION)
PKG     := github.com/Ibtesam-Mahmood/tailvault
LDFLAGS := -X $(PKG)/internal/version.Version=$(VERSION)

.PHONY: build test vet fmt
build:
	go build -ldflags "$(LDFLAGS)" -o bin/tailvault ./cmd/tailvault
test:
	go test ./...
vet:
	go vet ./...
fmt:
	gofmt -l .
