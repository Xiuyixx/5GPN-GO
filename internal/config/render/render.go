// Package render turns a validated config.Config into on-disk configuration
// files for the third-party data-plane components (dnsdist / mihomo / sniproxy).
//
// M0 skeleton — each renderer stub returns ErrNotImplemented; M1 lands the real
// templating.
package render

import "errors"

// ErrNotImplemented is returned by every M0 renderer stub.
var ErrNotImplemented = errors.New("renderer not implemented in M0")
