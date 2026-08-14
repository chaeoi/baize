GO ?= go
VERSION ?= $(shell date -u +%Y%m%d)
LDFLAGS = -s -w -buildid= -X main.version=$(VERSION)
BUILD_FLAGS = -trimpath -buildvcs=false -tags netgo,osusergo -ldflags="$(LDFLAGS)"

.PHONY: test build agent-arm64 agent-amd64 dashboard docker clean

test:
	$(GO) test ./...

build: agent-arm64 agent-amd64 dashboard

agent-arm64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(BUILD_FLAGS) -o dist/baize-agent-linux-arm64 ./agent/cmd/baize-agent

agent-amd64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o dist/baize-agent-linux-amd64 ./agent/cmd/baize-agent


dashboard:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o dist/baize-linux-amd64 ./dashboard/backend/cmd/baize

docker:
	docker build --build-arg VERSION=$(VERSION) -t baize:$(VERSION) -f dashboard/backend/Dockerfile .

clean:
	rm -f dist/baize-agent-* dist/baize-*
