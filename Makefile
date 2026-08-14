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
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(BUILD_FLAGS) -o dist/echobot-agent-linux-arm64 ./agent/cmd/echobot-agent

agent-amd64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o dist/echobot-agent-linux-amd64 ./agent/cmd/echobot-agent


dashboard:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o dist/echobot-linux-amd64 ./dashboard/backend/cmd/echobot

docker:
	docker build --build-arg VERSION=$(VERSION) -t echobot:$(VERSION) -f dashboard/backend/Dockerfile .

clean:
	rm -f dist/echobot-agent-* dist/echobot-*
