package web

import (
	"embed"
)

//go:embed dist/*
var WebResources embed.FS
