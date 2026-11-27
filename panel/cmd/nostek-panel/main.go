// Command nostek-panel runs the Nostek Panel web UI and its admin helpers.
//
//	nostek-panel serve
//	nostek-panel setup-link      (first admin, one-time link, 30 min)
//	nostek-panel admin-rotate EMAIL  (new random password via one-time link, 15 min)
//	nostek-panel healthcheck     (container health probe, exit 0 when /healthz answers ok)
//	nostek-panel version
package main

import (
	"github.com/thuekx/docker-panel-VPS/panel/app"
	"github.com/thuekx/docker-panel-VPS/panel/ext"
)

var version = "dev"

func main() { app.Main(version, ext.Extensions{}) }
