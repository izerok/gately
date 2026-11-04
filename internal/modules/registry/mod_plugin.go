//go:build !no_plugin

package registry

import (
	"github.com/wangwanjie/gately/internal/module"
	"github.com/wangwanjie/gately/internal/modules/plugin"
)

func init() {
	register(part{id: plugin.ID, order: order(plugin.ID), create: func(*wiring) module.Module { return plugin.New() }})
}
