package main

import (
	"fmt"
	"os"
	"zipreport-server/pkg/browser"
)

func main() {
	p, err := browser.Download()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(p)
}
