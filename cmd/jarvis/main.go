// Command jarvis est le point d'entrée CLI du pipeline d'extraction PDF.
package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "jarvis:", err)
		os.Exit(1)
	}
}
