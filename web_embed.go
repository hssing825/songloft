//go:build !lite

package main

import (
	"embed"
)

//go:embed all:songloft-player-build/web-embedded
var WebDist embed.FS
