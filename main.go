package main

import (
	"embed"

	"github.com/songquanpeng/one-api/cmd"
)

//go:embed web/build/*
var buildFS embed.FS

func init() {
	cmd.SetBuildFS(buildFS)
}

func main() {
	cmd.Execute()
}
