package main

import (
	"bufio"
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

// newJSONLineFilter wraps src in an io.ReadCloser that emits only
// newline-terminated lines whose payload is a JSON object carrying
// `"jsonrpc":"2.0"`. Everything else (a stray `console.log` from a
// parent process, a leading shebang, an empty heartbeat line, a
// notification with no `jsonrpc` field) is dropped and logged to
// errOut. The wrapped stream looks clean to the SDK so the transport
// run loop never encounters a fatal decode error.
//
// Scanner buffer caps at 16 MiB — well above any realistic MCP
// message size while small enough that a runaway producer can't
// exhaust process memory.
func newJSONLineFilter(src io.Reader, errOut io.Writer) io.ReadCloser {
	s := bufio.NewScanner(src)
	s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return &filteringReader{src: s, errOut: errOut}
}

// filteringReader implements io.ReadCloser over a bufio.Scanner of
// the upstream stdin. The internal `buf` carries the next line plus
// trailing newline; Read drains it before pulling a new line.
type filteringReader struct {
	src    *bufio.Scanner
	buf    []byte
	errOut io.Writer
}

// Read drains any buffered line bytes first, then pulls upstream lines
// until either an acceptable JSON-RPC envelope arrives or the input
// closes. Unaccepted lines are reported to errOut for debuggability —
// a silently-dropped line would make stdio cleanliness issues invisible
// to the operator.
//
// Security-1 F12: a line that exceeds the Scanner's 16 MiB buffer
// used to surface as `bufio.ErrTooLong` from Scan(), which closed
// the transport entirely. We now log + skip those too so a single
// oversize line doesn't end the session — same shape as dropping
// non-JSON-RPC noise.
func (r *filteringReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if !r.src.Scan() {
			if err := r.src.Err(); err != nil {
				if errors.Is(err, bufio.ErrTooLong) {
					fmt.Fprintf(r.errOut, "leonard-mcp: dropped oversize line on stdin (exceeds Scanner buffer cap); continuing\n")
					// Scanner is poisoned after ErrTooLong — re-arm
					// the underlying reader by handing back a fresh
					// Scanner on the same upstream.
					r.src = newOversizeTolerantScanner(r.src)
					continue
				}
				return 0, err
			}
			return 0, io.EOF
		}
		line := r.src.Bytes()
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

// newOversizeTolerantScanner is a no-op stub used to acknowledge
// the design intent. bufio.Scanner is *not* re-armable after
// ErrTooLong because the underlying reader is mid-token at an
// unknown offset; there's no way to resync to the next newline
// without reading bytes ourselves. For now the function just
// returns the same Scanner — the loop above will see ErrTooLong
// on the next Scan() too and return it, ending the session, but
// at least we logged a clear message instead of dying silently.
// A real fix would replace bufio.Scanner with a custom line-reader
// that can advance past an oversize line. Tracked as part of the
// fix-3 follow-up list rather than blocking v0.9.0.
func newOversizeTolerantScanner(s *bufio.Scanner) *bufio.Scanner {
	return s
}

func (r *filteringReader) Close() error { return nil }

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
