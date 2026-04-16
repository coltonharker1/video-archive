BINARY = va
CMD = ./cmd/va

.PHONY: build clean test

build:
	go build -o $(BINARY) $(CMD)

clean:
	rm -f $(BINARY)

test:
	go test ./...
