build:
	go build ./...

# Run unit tests
test:
	env "GORACE=halt_on_error=1" go test -v -race ./...

# Format the code
fix:
	find . -iname '*.go' -not -path '*/vendor/*' -print0 | xargs -0 gofmt -s -w
	find . -iname '*.go' -not -path '*/vendor/*' -print0 | xargs -0 goimports -w

# Lint the code
lint:
	gometalinter ./...


# ci installs dep by direct version.  Users install with 'go get'
setup_ci:
	go get -u github.com/alecthomas/gometalinter
	gometalinter --install

