.PHONY: build test run clean

build:
	go build -o omp-im ./cmd/omp-im

test:
	go test ./...

run: build
	./omp-im

clean:
	rm -f omp-im
