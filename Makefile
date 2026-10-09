.PHONY: build test install

build:
	go build -o gh-fleet .

test:
	go test ./...

# Install as a gh extension from this checkout, run as gh fleet.
install: build
	gh extension remove fleet 2>/dev/null || true
	gh extension install .
