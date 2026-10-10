.PHONY: build test install

build:
	go build -o gh-tidy .

test:
	go test ./...

# Install as a gh extension from this checkout, run as gh tidy.
install: build
	gh extension remove tidy 2>/dev/null || true
	gh extension install .
