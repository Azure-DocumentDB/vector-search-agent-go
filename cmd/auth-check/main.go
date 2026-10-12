package main

import (
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/app"
	"os"
)

func main() { os.Exit(app.Main("auth-check", os.Stdout, os.Stderr)) }
