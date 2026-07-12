package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
)

// Op captures one filesystem or process action taken by RecordingExecutor.
// The recorder backs both `--dry-run` (log then return nil) and the unit
// tests (assert against Ops).
type Op struct {
	Kind string // run | write | mkdir | remove | chown | chmod
	Path string
	Cmd  string
	Args []string
	Mode os.FileMode
	Size int
}

// RecordingExecutor collects operations without touching the host.
// When Apply is false it behaves as a pure dry-run.
type RecordingExecutor struct {
	mu       sync.Mutex
	Ops      []Op
	Files    map[string][]byte
	Dirs     map[string]bool
	Owners   map[string]string
	Modes    map[string]os.FileMode
	Existing map[string]bool
	Apply    bool // if true, mirror writes into an in-memory FS (still no host mutation)
	Failures map[string]error
	Out      io.Writer
}

// NewRecorder returns a fresh recording executor.
func NewRecorder() *RecordingExecutor {
	return &RecordingExecutor{
		Files:    map[string][]byte{},
		Dirs:     map[string]bool{},
		Owners:   map[string]string{},
		Modes:    map[string]os.FileMode{},
		Existing: map[string]bool{},
		Failures: map[string]error{},
	}
}

func (r *RecordingExecutor) record(op Op) {
	r.mu.Lock()
	r.Ops = append(r.Ops, op)
	r.mu.Unlock()
	if r.Out != nil {
		switch op.Kind {
		case "run":
			_, _ = fmt.Fprintf(r.Out, "  → run: %s %s\n", op.Cmd, strings.Join(op.Args, " "))
		case "write":
			_, _ = fmt.Fprintf(r.Out, "  → write: %s (%d bytes, mode %o)\n", op.Path, op.Size, op.Mode)
		case "mkdir":
			_, _ = fmt.Fprintf(r.Out, "  → mkdir: %s (mode %o)\n", op.Path, op.Mode)
		case "remove":
			_, _ = fmt.Fprintf(r.Out, "  → remove: %s\n", op.Path)
		case "chown":
			_, _ = fmt.Fprintf(r.Out, "  → chown: %s → %s\n", op.Path, op.Cmd)
		case "chmod":
			_, _ = fmt.Fprintf(r.Out, "  → chmod: %s → %o\n", op.Path, op.Mode)
		}
	}
}

func (r *RecordingExecutor) fail(key string) error {
	if r.Failures == nil {
		return nil
	}
	return r.Failures[key]
}

func (r *RecordingExecutor) Run(_ context.Context, name string, args ...string) error {
	r.record(Op{Kind: "run", Cmd: name, Args: args})
	return r.fail("run:" + name)
}

func (r *RecordingExecutor) WriteFile(path string, data []byte, mode os.FileMode) error {
	r.record(Op{Kind: "write", Path: path, Mode: mode, Size: len(data)})
	if r.Apply {
		r.Files[path] = append([]byte(nil), data...)
		r.Existing[path] = true
	}
	return r.fail("write:" + path)
}

func (r *RecordingExecutor) MkdirAll(path string, mode os.FileMode) error {
	r.record(Op{Kind: "mkdir", Path: path, Mode: mode})
	if r.Apply {
		r.Dirs[path] = true
		r.Existing[path] = true
	}
	return r.fail("mkdir:" + path)
}

func (r *RecordingExecutor) Remove(path string) error {
	r.record(Op{Kind: "remove", Path: path})
	if r.Apply {
		delete(r.Files, path)
		delete(r.Dirs, path)
		delete(r.Existing, path)
	}
	return r.fail("remove:" + path)
}

func (r *RecordingExecutor) Chown(path, user, group string) error {
	r.record(Op{Kind: "chown", Path: path, Cmd: user + ":" + group})
	if r.Apply {
		r.Owners[path] = user + ":" + group
	}
	return r.fail("chown:" + path)
}

func (r *RecordingExecutor) Chmod(path string, mode os.FileMode) error {
	r.record(Op{Kind: "chmod", Path: path, Mode: mode})
	if r.Apply {
		r.Modes[path] = mode
	}
	return r.fail("chmod:" + path)
}

func (r *RecordingExecutor) Exists(path string) bool {
	return r.Existing[path]
}

// Kinds returns a deterministic list of (kind, path) tuples — used by tests.
func (r *RecordingExecutor) Kinds() []string {
	out := make([]string, len(r.Ops))
	for i, op := range r.Ops {
		key := op.Kind + ":"
		if op.Path != "" {
			key += op.Path
		} else if op.Cmd != "" {
			key += op.Cmd
		}
		out[i] = key
	}
	sort.Strings(out)
	return out
}
