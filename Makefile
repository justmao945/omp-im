.PHONY: build test run clean install install-user

build:
	go build -o omp-im ./cmd/omp-im

test:
	go test ./...

run: build
	./omp-im

clean:
	rm -f omp-im

PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin

install: build
	install -m 755 omp-im $(BINDIR)/

install-user: build
	install -d $(HOME)/.local/bin
	install -m 755 omp-im $(HOME)/.local/bin/
