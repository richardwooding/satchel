//go:build !(js && wasm)

// Non-wasm stub so `go build ./...` and `go vet ./...` succeed natively.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "satchel-wasm is the browser core; build it with:")
	fmt.Fprintln(os.Stderr, "  GOOS=js GOARCH=wasm go build -o web/dist/satchel.wasm ./cmd/satchel-wasm")
	os.Exit(1)
}
