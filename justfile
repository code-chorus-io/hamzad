default:
    @just --list

build:
    @echo 'building koochooloologin'
    go build -o koochooloologin ./cmd/koochooloologin

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
