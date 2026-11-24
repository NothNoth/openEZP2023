GOROOT_WASM := $(shell go env GOROOT)/misc/wasm/wasm_exec.js

.PHONY: all wasm dist serve test clean

all: wasm
	go build -o bin/ezp-serve ./cmd/ezp-serve

wasm:
	GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o web/ezp.wasm ./cmd/ezp-wasm
	cp "$(GOROOT_WASM)" web/wasm_exec.js

# dist/ is a static folder to upload to any web server with HTTPS.
# File references get a ?v=<hash> suffix so browsers never mix versions.
dist: wasm
	rm -rf dist
	mkdir -p dist/fonts
	cp web/index.html web/app.js web/wasm_exec.js web/ezp.wasm dist/
	cp web/fonts/* dist/fonts/
	cp deploy/htaccess dist/.htaccess
	v=$$(cat web/ezp.wasm web/app.js web/wasm_exec.js web/index.html | sha256sum | cut -c1-10); \
	sed -i "s/\"ezp\.wasm\"/\"ezp.wasm?v=$$v\"/" dist/app.js; \
	sed -i "s/src=\"app\.js\"/src=\"app.js?v=$$v\"/; s/src=\"wasm_exec\.js\"/src=\"wasm_exec.js?v=$$v\"/" dist/index.html; \
	echo "dist/ ready (version $$v)"

serve: wasm
	go run ./cmd/ezp-serve

test:
	go test ./ezp/...

clean:
	rm -rf bin dist web/ezp.wasm web/wasm_exec.js
