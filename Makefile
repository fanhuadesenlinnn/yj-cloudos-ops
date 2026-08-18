BIN_DIR := build
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all linux windows tidy clean

all: linux windows

linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/yj-cloudos-ops-linux-amd64 .

windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/yj-cloudos-ops-windows-amd64.exe .

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR)
