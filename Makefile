APP_NAME := MemVRAM
BIN_DIR := bin
BIN_PATH := $(BIN_DIR)/$(APP_NAME)

.PHONY: run build test clean

run:
	go run .

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_PATH) .

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)
