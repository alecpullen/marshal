package main

import (
	"context"
	"fmt"
	"os"

	"marshal/internal/app"
)

func main() {
	if err := app.Run(context.Background(), os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
}
