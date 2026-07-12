.PHONY: build test run clean

build:
	go build -o omp-im ./cmd/omp-im

test:
	go test ./...

run: build
	./omp-im -config ~/.omp-im/config.json

clean:
	rm -f omp-im
