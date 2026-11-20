.PHONY: build install fmt vet

build:
	go build -o gh-fleet .

install:
	go install .

fmt:
	gofmt -w main.go

vet:
	go vet ./...
