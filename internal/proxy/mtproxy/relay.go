package mtproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
)

// Relay pipes bytes between a client (obfuscated2 with secret) and a
// Telegram DC (obfuscated2 without secret). Four AES-CTR streams run
// concurrently — one for each direction on each side — decrypting on
// entry and re-encrypting on exit so the two obfuscation layers stay
// isolated end-to-end.
//
// Wire model:
//
//	client       [obf2 with secret]      proxy       [obf2 no secret]     DC
//	  ─── decrypt via cs.StreamRecv ───►    │
//	                                       │
//	                                       │  encrypt via us.StreamSend
//	                                       │  ────────────────────────────►
//	                                       │
//	                                       │  decrypt via us.StreamRecv
//	                                       ◄─────────────────────────────
//	  ◄── encrypt via cs.StreamSend ─────    │
//
// Neither side needs to understand the plaintext MTProto framing; the
// proxy is a byte pipe with two cipher layers, and framing decoding
// is the client's and DC's problem.
type Relay struct {
	clientConn net.Conn
	upstream   net.Conn
	client     *ClientSession
	upstream2  *UpstreamSession

	// bufSize is the read chunk size on both directions. 16 KiB is
	// slightly larger than a padded intermediate frame and small
	// enough to keep memory flat under a scanner storm; io.Copy's
	// default 32 KiB is overkill for MTProto's small messages.
	bufSize int
}

const defaultRelayBuf = 16 * 1024

// NewRelay wires the four cipher streams around the two conns. The
// caller is responsible for having already written the client's
// received handshake bytes' "consumed" state (i.e. cs.StreamRecv
// already advanced by 64 bytes inside DecodeClientHandshake) and for
// having sent us.Header to the upstream (see Start).
func NewRelay(clientConn, upstream net.Conn, cs *ClientSession, us *UpstreamSession) *Relay {
	return &Relay{
		clientConn: clientConn,
		upstream:   upstream,
		client:     cs,
		upstream2:  us,
		bufSize:    defaultRelayBuf,
	}
}

// Run copies bytes both ways until one side closes or ctx cancels.
// Returns nil on graceful client/upstream EOF, or the first non-EOF
// error observed. Both conns are closed before return.
func (r *Relay) Run(ctx context.Context) error {
	defer r.clientConn.Close()
	defer r.upstream.Close()

	// Cancel the copies as soon as ctx dies, by unblocking both
	// reads via a socket close.
	ctxDone := make(chan struct{})
	defer close(ctxDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = r.clientConn.Close()
			_ = r.upstream.Close()
		case <-ctxDone:
		}
	}()

	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	// client → upstream: decrypt from client (StreamRecv), encrypt
	// to upstream (upstream2.StreamSend).
	go func() {
		defer wg.Done()
		err := r.pipe(r.clientConn, r.upstream, r.client.StreamRecv, r.upstream2.StreamSend)
		errCh <- err
	}()
	// upstream → client: decrypt from upstream (upstream2.StreamRecv),
	// encrypt to client (client.StreamSend).
	go func() {
		defer wg.Done()
		err := r.pipe(r.upstream, r.clientConn, r.upstream2.StreamRecv, r.client.StreamSend)
		errCh <- err
	}()

	wg.Wait()
	close(errCh)
	for e := range errCh {
		if e != nil && !isBenignCloseError(e) {
			return e
		}
	}
	return nil
}

// pipe is one leg of the byte pump: read from src, XOR through
// decrypt (undoing the incoming side's obfuscation), XOR through
// encrypt (applying the outgoing side's obfuscation), write to dst.
// Doing decrypt+encrypt in a single buffer avoids a second allocation
// on the hot path.
func (r *Relay) pipe(src io.Reader, dst io.Writer, decrypt, encrypt interface {
	XORKeyStream(dst, src []byte)
}) error {
	buf := make([]byte, r.bufSize)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			decrypt.XORKeyStream(buf[:n], buf[:n])
			encrypt.XORKeyStream(buf[:n], buf[:n])
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// isBenignCloseError filters out the two error patterns that always
// show up on the losing side of an intentional close: net.ErrClosed
// (our own Close()) and "use of closed network connection" wrapped in
// legacy paths.
func isBenignCloseError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
		return true
	}
	return false
}
