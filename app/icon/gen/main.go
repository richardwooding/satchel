// Command gen writes the app icon at a given size from package icon:
//
//	go run ./icon/gen build/appicon.png          # 1024px
//	go run ./icon/gen build/linux/satchel-256.png 256
package main

import (
	"log"
	"os"
	"strconv"

	"github.com/richardwooding/satchel/app/icon"
)

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		log.Fatal("usage: gen <out.png> [size]")
	}
	size := 1024
	if len(os.Args) == 3 {
		n, err := strconv.Atoi(os.Args[2])
		if err != nil || n < 16 || n > 4096 {
			log.Fatalf("bad size %q", os.Args[2])
		}
		size = n
	}
	if err := os.WriteFile(os.Args[1], icon.App(size), 0o644); err != nil {
		log.Fatal(err)
	}
}
