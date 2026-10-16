package main

import (
	"ccguard/internal/guard"
	"os"
)

func main() { os.Exit(guard.Main(os.Args[1:])) }
