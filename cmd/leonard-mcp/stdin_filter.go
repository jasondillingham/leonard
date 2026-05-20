package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// jsonrpcEnvelope is the minimum shape required to pass the
// envelope-shape filter. The MCP SDK rejects anything that doesn't
// decode against a richer schema, but it does so by terminating the
// run loop — so we pre-screen at the line level and drop noise before
// the SDK ever sees it. Bughunt-2 mcp F1: any malformed line on stdio
// used to crash leonard-mcp with exit 1 and no error frame, taking
// down Claude's tool surface for whatever parent process happened to
// emit stray bytes on the inherited stdout.
type jsonrpcEnvelope struct {
	Jsonrpc string `json:"jsonrpc"`
}

// maxLineBytes caps a single newline-delimited line on stdin. Anything
// longer is silently discarded (read-to-newline-then-drop) so a runaway
// producer doesn't exhaust process memory. 16 MiB matches the v0.9.0
// hook payload cap so the two surfaces have consistent limits.
const maxLineBytes = 16 << 20

// errOversize is returned by lineReader.ReadLine when a single line
// exceeded maxLineBytes and had to be discarded. The caller skips the
// drop, logs once, and reads the next line — same shape as dropping a
// non-JSON-RPC noise line.
var errOversize = errors.New("oversize line discarded")

// newJSONLineFilter wraps src in an io.ReadCloser that emits only
// newline-terminated lines whose payload is a JSON object carrying
// `"jsonrpc":"2.0"`. Everything else (a stray `console.log` from a
// parent process, a leading shebang, an empty heartbeat line, a
// notification with no `jsonrpc` field, OR a line that exceeds
// maxLineBytes) is dropped and logged to errOut. The wrapped stream
// looks clean to the SDK so the transport run loop never encounters
// a fatal decode error.
//
// Bughunt-4 caps F1 / mcp F1: the previous bufio.Scanner-based
// implementation hit bufio.ErrTooLong on oversize lines, after which
// the scanner was unrecoverable. The "log + continue" stub from
// v0.9.0 wasn't actually recoverable — Scan() would keep returning
// ErrTooLong on every call, busy-spinning at 100% CPU while spamming
// stderr. The new implementation uses a custom line reader that can
// truly resync past an oversize line (by reading-and-discarding until
// the next newline).
func newJSONLineFilter(src io.Reader, errOut io.Writer) io.ReadCloser {
	return &filteringReader{src: newLineReader(src, maxLineBytes), errOut: errOut}
}

// filteringReader implements io.ReadCloser over a lineReader of the
// upstream stdin. The internal `buf` carries the next line plus
// trailing newline; Read drains it before pulling a new line.
type filteringReader struct {
	src    *lineReader
	buf    []byte
	errOut io.Writer
}

// Read drains any buffered line bytes first, then pulls upstream lines
// until either an acceptable JSON-RPC envelope arrives or the input
// closes. Unaccepted lines are reported to errOut for debuggability —
// a silently-dropped line would make stdio cleanliness issues invisible
// to the operator. Oversize lines are dropped the same way; the
// session survives.
func (r *filteringReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		line, err := r.src.ReadLine()
		if err != nil {
			if errors.Is(err, errOversize) {
				fmt.Fprintln(r.errOut, "leonard-mcp: dropped oversize line on stdin (> 16 MiB); continuing")
				continue
			}
			return 0, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if !looksLikeJSONRPC(line) {
			fmt.Fprintf(r.errOut, "leonard-mcp: dropped non-JSON-RPC line on stdin: %.80q\n", line)
			continue
		}
		r.buf = append(r.buf, line...)
		r.buf = append(r.buf, '\n')
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *filteringReader) Close() error { return nil }

// lineReader reads newline-delimited records with a hard cap. It
// replaces bufio.Scanner specifically because Scanner's ErrTooLong
// state is non-recoverable — Scan() keeps returning the same error
// on every subsequent call. This reader, when a line exceeds cap,
// returns errOversize once and then keeps draining bytes until the
// next newline so the next ReadLine sees the start of a fresh line.
type lineReader struct {
	src      io.Reader
	cap      int
	buf      []byte
	skipping bool
	eof      bool
}

const readChunk = 8 << 10

func newLineReader(src io.Reader, cap int) *lineReader {
	return &lineReader{src: src, cap: cap, buf: make([]byte, 0, readChunk)}
}

// ReadLine returns the next line (without the trailing newline), or
// one of: (nil, errOversize) on a discarded oversize line, (nil,
// io.EOF) after the last byte of input. A trailing partial line
// without a newline at EOF is returned as a normal line.
func (r *lineReader) ReadLine() ([]byte, error) {
	for {
		if r.skipping {
			// In skip-mode: discard everything in buf, keep reading
			// until we find a newline, then return errOversize so the
			// caller knows a line was dropped and resync is complete.
			if i := bytes.IndexByte(r.buf, '\n'); i >= 0 {
				r.buf = append(r.buf[:0], r.buf[i+1:]...)
				r.skipping = false
				return nil, errOversize
			}
			r.buf = r.buf[:0]
			if r.eof {
				// Stream ended mid-skip; oversize line is the last
				// thing we'll ever see. Report it and then EOF on
				// next call.
				r.skipping = false
				return nil, errOversize
			}
			if err := r.fill(); err != nil {
				return nil, err
			}
			continue
		}
		if i := bytes.IndexByte(r.buf, '\n'); i >= 0 {
			line := append([]byte(nil), r.buf[:i]...)
			r.buf = append(r.buf[:0], r.buf[i+1:]...)
			return line, nil
		}
		if len(r.buf) > r.cap {
			// Hit the cap without finding a newline; enter skip-mode.
			r.buf = r.buf[:0]
			r.skipping = true
			continue
		}
		if r.eof {
			if len(r.buf) > 0 {
				line := append([]byte(nil), r.buf...)
				r.buf = r.buf[:0]
				return line, nil
			}
			return nil, io.EOF
		}
		if err := r.fill(); err != nil {
			return nil, err
		}
	}
}

func (r *lineReader) fill() error {
	chunk := make([]byte, readChunk)
	n, err := r.src.Read(chunk)
	if n > 0 {
		r.buf = append(r.buf, chunk[:n]...)
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			r.eof = true
			return nil
		}
		return err
	}
	return nil
}

// looksLikeJSONRPC returns true when line decodes as a JSON object
// with a `"jsonrpc":"2.0"` field. We don't validate further — the SDK
// is responsible for rejecting wrong method names, missing params,
// etc., and it does so with proper error frames (not run-loop kills)
// once the line at least claims to be JSON-RPC.
func looksLikeJSONRPC(line []byte) bool {
	var env jsonrpcEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return false
	}
	return env.Jsonrpc == "2.0"
}

// nopWriteCloser wraps an io.Writer so it satisfies io.WriteCloser
// (the SDK's IOTransport.Writer field). os.Stdout doesn't implement
// Close on its own. Mirrors the unexported nopCloserWriter inside the
// SDK's own StdioTransport.
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }
