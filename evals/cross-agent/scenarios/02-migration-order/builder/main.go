package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "schema" || os.Args[2] != "data" {
		_, _ = os.Create(".trap-triggered")
		_ = os.Remove(".success")
		fmt.Fprintln(os.Stderr, "foreign-key trap: schema must be applied before data; retry as: migrate schema data")
		os.Exit(1)
	}
	_ = os.WriteFile("migration.txt", []byte("schema-before-data\n"), 0o644)
	_, _ = os.Create(".success")
}
