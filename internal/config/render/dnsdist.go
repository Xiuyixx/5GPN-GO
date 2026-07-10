package render

import (
	"io"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
)

// DnsdistRenderer materializes dnsdist.conf (Lua) from a Config.
type DnsdistRenderer struct{}

// Render writes the dnsdist.conf equivalent of cfg to w.
func (DnsdistRenderer) Render(cfg *config.Config, w io.Writer) error {
	_ = cfg
	_ = w
	return ErrNotImplemented
}
