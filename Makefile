UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
LIB_EXT := dylib
else
LIB_EXT := so
endif

.PHONY: build test live-test
build:
	mkdir -p build
	CGO_ENABLED=1 go build -buildmode=c-shared -o build/jev-adaptive-thinking.$(LIB_EXT) ./cmd/plugin

test:
	go test -race ./... -timeout 60s

live-test:
	JEV_LIVE_TEST=1 go test -v ./internal/router -run '^TestLiveJev$$' -count=1
