GOROOT := $(shell go env GOROOT)

.PHONY: web wasm serve test lint clean

web:
	mkdir -p web/dist
	cp web/src/*.html web/src/*.js web/src/*.css web/src/*.svg web/dist/
	cp "$(GOROOT)/lib/wasm/wasm_exec.js" web/dist/

wasm: web
	GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o web/dist/satchel.wasm ./cmd/satchel-wasm
	go run ./cmd/compress-assets web/dist

serve: wasm
	go run ./cmd/satchel-relay

test:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run

clean:
	rm -f satchel-relay
	find web/dist -type f ! -name .gitkeep -delete
