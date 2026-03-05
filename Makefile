.PHONY: build test gen clean

build:
	go build ./...

test:
	go test ./... -count=1 -race

gen:
	go run ./cmd/arcana-gen -output ./sdk/generated/

clean:
	rm -rf ./sdk/generated/
