APP_NAME := MemVRAM
BIN_DIR := bin
BIN_PATH := $(BIN_DIR)/$(APP_NAME)
APP_BUNDLE := $(BIN_DIR)/$(APP_NAME).app
APP_CONTENTS := $(APP_BUNDLE)/Contents
APP_MACOS := $(APP_CONTENTS)/MacOS
APP_RESOURCES := $(APP_CONTENTS)/Resources

.PHONY: run build app test clean

run:
	go run .

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_PATH) .

app: build
	mkdir -p $(APP_MACOS) $(APP_RESOURCES)
	cp $(BIN_PATH) $(APP_MACOS)/$(APP_NAME)
	chmod +x $(APP_MACOS)/$(APP_NAME)
	cp packaging/Info.plist $(APP_CONTENTS)/Info.plist

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)
