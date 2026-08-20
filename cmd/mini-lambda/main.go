package main

import (
	"fmt"
	"os"

	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/root"
)

func main() {
	if err := root.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
