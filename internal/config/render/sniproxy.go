package render

import (
	"io"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
)

// SniproxyRenderer materializes sniproxy.conf from a Config.
type SniproxyRenderer struct{}

// Render writes the sniproxy.conf equivalent of cfg to w.
func (SniproxyRenderer) Render(cfg *config.Config, w io.Writer) error {
	_ = cfg
	_ = w
	return ErrNotImplemented
}
