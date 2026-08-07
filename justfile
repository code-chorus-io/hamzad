default:
    @just --list

build:
    @echo 'building koochooloologin'
    go build -tags with_quic,with_wireguard -o koochooloologin ./cmd/koochooloologin

run *ARGS:
    go run ./cmd/koochooloologin {{ ARGS }}

test:
    go test -race -v ./... -covermode=atomic -coverprofile=coverage.out

lint:
    golangci-lint run -c .golangci.yml

tidy:
    go mod tidy

update:
    @cd ./cmd/koochooloologin && go get -u

# validate the release configuration
release-check:
    goreleaser check

# build the release archives locally, publishing nothing
snapshot:
    goreleaser release --snapshot --clean --skip=publish
