// Package ascii embeds the Wedjat ASCII art for CLI startup banners and TUI.
package ascii

import _ "embed"

//go:embed eye.txt
var Eye string

//go:embed ankh.txt
var Ankh string

//go:embed pyramid-scene.txt
var PyramidScene string

//go:embed pyramid-solid.txt
var PyramidSolid string

//go:embed mask.txt
var Mask string