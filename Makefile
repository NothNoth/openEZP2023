# Works with GNU make on Linux and macOS (BSD sed, no sha256sum required).

GOROOT    := $(shell go env GOROOT)
# Go 1.24+ ships wasm_exec.js in lib/wasm, older versions in misc/wasm.
WASM_EXEC := $(firstword $(wildcard $(GOROOT)/lib/wasm/wasm_exec.js $(GOROOT)/misc/wasm/wasm_exec.js))

.PHONY: all wasm dist serve test clean

all: wasm
	go build -o bin/ezp-serve ./cmd/ezp-serve

wasm:
	@test -n "$(WASM_EXEC)" || { echo "wasm_exec.js not found under $(GOROOT)" >&2; exit 1; }
	GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o web/ezp.wasm ./cmd/ezp-wasm
	cp "$(WASM_EXEC)" web/wasm_exec.js

# dist/ is a static folder to upload to any web server with HTTPS.
# File references get a ?v=<hash> suffix so browsers never mix versions.
# sed writes to a temporary file instead of using -i, whose syntax differs
# between GNU sed (Linux) and BSD sed (macOS).
dist: wasm
	rm -rf dist
	mkdir -p dist/fonts
	cp web/index.html web/app.js web/wasm_exec.js web/ezp.wasm dist/
	cp web/fonts/* dist/fonts/
	cp deploy/htaccess dist/.htaccess
	@set -e; \
	if command -v sha256sum >/dev/null 2>&1; then hash="sha256sum"; else hash="shasum -a 256"; fi; \
	v=$$(cat web/ezp.wasm web/app.js web/wasm_exec.js web/index.html | $$hash | cut -c1-10); \
	sed "s/\"ezp\.wasm\"/\"ezp.wasm?v=$$v\"/" dist/app.js > dist/app.js.tmp; \
	mv dist/app.js.tmp dist/app.js; \
	sed -e "s/src=\"app\.js\"/src=\"app.js?v=$$v\"/" \
	    -e "s/src=\"wasm_exec\.js\"/src=\"wasm_exec.js?v=$$v\"/" dist/index.html > dist/index.html.tmp; \
	mv dist/index.html.tmp dist/index.html; \
	grep -q "ezp.wasm?v=$$v" dist/app.js || { echo "versioning of app.js failed" >&2; exit 1; }; \
	grep -q "app.js?v=$$v" dist/index.html || { echo "versioning of index.html failed" >&2; exit 1; }; \
	echo "dist/ ready (version $$v)"

serve: wasm
	go run ./cmd/ezp-serve

test:
	go test ./ezp/...

clean:
	rm -rf bin dist web/ezp.wasm web/wasm_exec.js
