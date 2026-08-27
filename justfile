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

# `go get -u` walks the whole import graph and moves indirect modules onto lines
# they do not belong to. The sagernet modules cut tags off sing-box's dev
# branch, so -u swaps in sing-quic v0.6.4, which wants sing v0.9 and no longer
# compiles against the sing v0.8 that sing-box v1.13 pins. Naming only the
# direct requirements leaves the rest to MVS and sing-box's own go.mod.
# ({{{{ is just's escape for a literal {{ in the Go template below.)
# update the direct requirements, letting MVS resolve everything else
update:
    go get $(go list -m -f '{{{{if and (not .Main) (not .Indirect)}}{{{{.Path}}@latest{{{{end}}' all)
    go mod tidy

# validate the release configuration
release-check:
    goreleaser check

# build the release archives locally, publishing nothing
snapshot:
    goreleaser release --snapshot --clean --skip=publish
