package ios

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxRequestHeaderBytes = 32 << 10
	maxRequestLineBytes   = 4 << 10
	maxHeaderLineBytes    = 8 << 10
	maxHeaderCount        = 100
)

// Route wires a URL path to a filesystem file + content type. Matches the
// two-route surface of 5GPN-X/ios-http.py.
type Route struct {
	Filename    string
	ContentType string
}

// DefaultRoutes mirrors the Python legacy routes exactly.
func DefaultRoutes() map[string]Route {
	return map[string]Route{
		"/ios-dot.mobileconfig": {Filename: "ios-dot.mobileconfig", ContentType: "application/x-apple-aspen-config"},
		"/":                     {Filename: "index.html", ContentType: "text/html; charset=utf-8"},
		"/index.html":           {Filename: "index.html", ContentType: "text/html; charset=utf-8"},
	}
}

// ServeConn handles a single inetd-style socket-activated connection. The
// remote is expected to send exactly one HTTP/1.x request. Times out after
// deadline; caller keeps the connection.
//
// deadline mirrors the Python legacy 10-second SIGALRM guard so no stalled
// socket can pin a live process.
func ServeConn(conn net.Conn, wwwDir string, routes map[string]Route, deadline time.Duration) error {
	if deadline <= 0 {
		deadline = 10 * time.Second
	}
	_ = conn.SetDeadline(time.Now().Add(deadline))

	limited := &io.LimitedReader{R: conn, N: maxRequestHeaderBytes + 1}
	r := bufio.NewReaderSize(limited, 8192)
	requestLine, err := r.ReadString('\n')
	if err != nil {
		writeStatus(conn, "400 Bad Request", "text/plain", []byte("bad request\n"))
		return err
	}
	if len(requestLine) > maxRequestLineBytes {
		writeStatus(conn, "414 URI Too Long", "text/plain", []byte("request line too large\n"))
		return errors.New("request line exceeds limit")
	}
	parts := strings.Fields(strings.TrimRight(requestLine, "\r\n"))
	if len(parts) < 2 || parts[0] != "GET" {
		writeStatus(conn, "400 Bad Request", "text/plain", []byte("bad request\n"))
		return fmt.Errorf("bad request line: %q", requestLine)
	}
	headerBytes := len(requestLine)
	for count := 0; ; count++ {
		if count >= maxHeaderCount {
			writeStatus(conn, "431 Request Header Fields Too Large", "text/plain", []byte("too many headers\n"))
			return errors.New("request header count exceeds limit")
		}
		line, err := r.ReadString('\n')
		headerBytes += len(line)
		if len(line) > maxHeaderLineBytes || headerBytes > maxRequestHeaderBytes || limited.N <= 0 {
			writeStatus(conn, "431 Request Header Fields Too Large", "text/plain", []byte("headers too large\n"))
			return errors.New("request headers exceed limit")
		}
		if err != nil {
			writeStatus(conn, "400 Bad Request", "text/plain", []byte("bad request\n"))
			return err
		}
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}

	path := strings.SplitN(parts[1], "?", 2)[0]
	route, ok := routes[path]
	if !ok {
		writeStatus(conn, "404 Not Found", "text/plain", []byte("not found\n"))
		return nil
	}
	body, err := os.ReadFile(filepath.Join(wwwDir, route.Filename))
	if err != nil {
		writeStatus(conn, "404 Not Found", "text/plain", []byte("not found\n"))
		return nil
	}
	writeStatus(conn, "200 OK", route.ContentType, body)
	return nil
}

func writeStatus(w io.Writer, status, ctype string, body []byte) {
	head := fmt.Sprintf("HTTP/1.1 %s\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		status, ctype, len(body))
	_, _ = w.Write([]byte(head))
	_, _ = w.Write(body)
}
