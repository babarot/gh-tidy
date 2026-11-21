.PHONY: build test check lint install demo

build:
	go build -o gh-tidy .

test:
	go test ./...

# What must pass before a change lands in main, and what CI runs.
check: lint
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt: the files above are not formatted"; exit 1; }
	go test -race ./...

# golangci-lint comes from mise.toml.
lint:
	golangci-lint run ./...
	go mod tidy -diff

# Install as a gh extension from this checkout, run as gh tidy.
install: build
	gh extension remove tidy 2>/dev/null || true
	gh extension install .

# Record the README's demo with VHS, against a made-up world (ADR-010):
# demo/recording/out/demo.gif, not kept in git.
demo:
	vhs demo/recording/tapes/demo.tape
