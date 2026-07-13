.PHONY: build test run clean install install-user pm2-start pm2-stop

build:
	go build -o omp-im ./cmd/omp-im

test:
	go test ./...

run: build
	./omp-im

clean:
	rm -f omp-im

pm2-start: install-user
	pm2 delete omp-im 2>/dev/null || true
	pm2 start ecosystem.config.js

pm2-stop:
	pm2 stop ecosystem.config.js

PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin

install: build
	install -m 755 omp-im $(BINDIR)/

install-user: build
	install -d $(HOME)/.local/bin
	install -m 755 omp-im $(HOME)/.local/bin/
