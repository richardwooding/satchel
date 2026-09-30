// Command gen writes build/appicon.png from package icon:
//
//	go run ./icon/gen build/appicon.png
package main

import (
	"log"
	"os"

	"github.com/richardwooding/satchel/app/icon"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: gen <out.png>")
	}
	if err := os.WriteFile(os.Args[1], icon.App(1024), 0o644); err != nil {
		log.Fatal(err)
	}
}
