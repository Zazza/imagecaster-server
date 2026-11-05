.PHONY: build run test vet clean

BINARY   := imagecaster-server
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -ldflags "-X main.version=$(VERSION)"

build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY) .

run: build
	./$(BINARY)

test:
	go test ./... -count=1 -timeout 120s

vet:
	go vet ./...

clean:
	rm -f $(BINARY)

