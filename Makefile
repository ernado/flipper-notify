# Companion Flipper app (vibration-only alert). Needs ufbt: pip install ufbt.
# The .fap must match the device firmware API; rebuild after a firmware update.
UFBT ?= ufbt

.PHONY: build fap install test

build:
	go build -o bin/fpager ./cmd/fpager

fap:
	cd fap/fpager_vibro && $(UFBT)
	cp fap/fpager_vibro/dist/fpager_vibro.fap internal/fap/fpager_vibro.fap

install:
	go build -o $(HOME)/.local/bin/fpager ./cmd/fpager
	systemctl --user restart fpager

test:
	go test -race ./...
