#!/bin/bash -ex

# Usage: ./hack/build.sh -o <binary> <package>
#   e.g. ./hack/build.sh -o bin/manager ./cmd/main.go
#   e.g. ./hack/build.sh -o bin/sbd-agent ./cmd/sbd-agent/main.go

GIT_VERSION=$(git describe --always --tags || true)
VERSION=${CI_VERSION:-${GIT_VERSION}}
GIT_COMMIT=$(git rev-list -1 HEAD || true)
COMMIT=${CI_COMMIT:-${GIT_COMMIT}}
BUILD_DATE=$(date --utc -Iseconds)

mkdir -p bin

LDFLAGS_VALUE="-X github.com/medik8s/sbd-operator/pkg/version.GitCommit=${COMMIT} "
LDFLAGS_VALUE+="-X github.com/medik8s/sbd-operator/pkg/version.GitDescribe=${VERSION} "
LDFLAGS_VALUE+="-X github.com/medik8s/sbd-operator/pkg/version.BuildDate=${BUILD_DATE} "
# allow override for debugging flags
LDFLAGS_DEBUG="${LDFLAGS_DEBUG:-" -s -w"}"
LDFLAGS_VALUE+="${LDFLAGS_DEBUG}"
# must be single quoted for use in GOFLAGS, and for more options see https://pkg.go.dev/cmd/link
LDFLAGS="'-ldflags=${LDFLAGS_VALUE}'"

export GOFLAGS+=" ${LDFLAGS}"
echo "goflags: ${GOFLAGS}"

# allow override and use zero by default (FIPS builds set CGO_ENABLED=1)
export CGO_ENABLED=${CGO_ENABLED:-0}
echo "cgo: ${CGO_ENABLED}"

# export in case it was set
export GOEXPERIMENT="${GOEXPERIMENT}"

GOOS=linux GOARCH=amd64 go build "$@"
