default:
    @just --list

build:
    @echo 'building hamzad'
    go build -tags with_quic,with_wireguard -o hamzad ./cmd/hamzad

run *ARGS:
    go run ./cmd/hamzad {{ ARGS }}

test:
    go test -race -v -tags with_quic,with_wireguard ./... -covermode=atomic -coverprofile=coverage.out

lint:
    golangci-lint run -c .golangci.yml

tidy:
    go mod tidy

update:
    @cd ./cmd/hamzad && go get -u

# validate the release configuration
release-check:
    goreleaser check

# build the release archives locally, publishing nothing
snapshot:
    goreleaser release --snapshot --clean --skip=publish
