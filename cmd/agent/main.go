package main

import (
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/app"
	"os"
)

func main() { os.Exit(app.Main("agent", os.Stdout, os.Stderr)) }
