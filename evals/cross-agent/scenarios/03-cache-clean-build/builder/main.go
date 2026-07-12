package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "--clean" {
		_, _ = os.Create(".trap-triggered")
		_ = os.Remove(".success")
		fmt.Fprintln(os.Stderr, "stale-cache trap: release builds require the --clean flag")
		os.Exit(1)
	}
	_ = os.WriteFile("build.txt", []byte("cache-state=clean\n"), 0o644)
	_, _ = os.Create(".success")
}
