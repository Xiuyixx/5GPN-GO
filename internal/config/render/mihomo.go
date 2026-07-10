package render

import (
	"io"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
)

// MihomoRenderer materializes mihomo config.yaml from a Config.
type MihomoRenderer struct{}

// Render writes the mihomo config.yaml equivalent of cfg to w.
func (MihomoRenderer) Render(cfg *config.Config, w io.Writer) error {
	_ = cfg
	_ = w
	return ErrNotImplemented
}
