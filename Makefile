.PHONY: build test demo image clean
build:
	mkdir -p bin
	go build -o bin/cloudmesh ./cmd/cloudmesh
	go build -o bin/echo ./cmd/echo
	go build -o bin/netlab ./cmd/netlab
test:
	go test -race ./...
	go vet ./...
demo: build
	bash scripts/demo.sh
image:
	docker build -t cloudmesh:local .
clean:
	rm -rf bin
