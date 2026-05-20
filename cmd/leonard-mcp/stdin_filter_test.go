package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// TestJSONLineFilter_DropsNonRPCLines covers bughunt-2 mcp F1: the
// filter must let valid JSON-RPC 2.0 envelopes through unchanged and
// drop everything else (raw garbage, valid JSON without `jsonrpc`,
// scalars, arrays).
func TestJSONLineFilter_DropsNonRPCLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // what the SDK should observe (lines passed through)
	}{
		{
			name: "well-formed request passes through",
			in:   `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n",
			want: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n",
		},
		{
			name: "garbage line dropped",
			in:   "not valid json\n",
			want: "",
		},
		{
			name: "JSON scalar dropped",
			in:   "42\n",
			want: "",
		},
		{
			name: "object without jsonrpc field dropped",
			in:   `{"id":1,"method":"tools/list"}` + "\n",
			want: "",
		},
		{
			name: "object with wrong jsonrpc version dropped",
			in:   `{"jsonrpc":"1.0","id":1,"method":"tools/list"}` + "\n",
			want: "",
		},
		{
			name: "empty lines silently skipped",
			in:   "\n\n\n",
			want: "",
		},
		{
			name: "garbage then valid: valid passes",
			in:   "console.log out of band\n" + `{"jsonrpc":"2.0","id":1,"method":"x"}` + "\n",
			want: `{"jsonrpc":"2.0","id":1,"method":"x"}` + "\n",
		},
		{
			name: "JSON array dropped",
			in:   `[1,2,3]` + "\n",
			want: "",
		},
		{
			name: "two valid lines both pass",
			in: `{"jsonrpc":"2.0","id":1,"method":"a"}` + "\n" +
				`{"jsonrpc":"2.0","id":2,"method":"b"}` + "\n",
			want: `{"jsonrpc":"2.0","id":1,"method":"a"}` + "\n" +
				`{"jsonrpc":"2.0","id":2,"method":"b"}` + "\n",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var errOut bytes.Buffer
			r := newJSONLineFilter(strings.NewReader(tc.in), &errOut)
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("filter output:\n got:  %q\n want: %q", got, tc.want)
			}
			// Dropped lines should be logged to errOut for visibility.
			droppedExpected := strings.Count(tc.in, "\n") - strings.Count(tc.want, "\n")
			gotDropped := strings.Count(errOut.String(), "dropped non-JSON-RPC")
			// Empty-input lines are silently skipped; only non-empty
			// dropped lines should appear in errOut.
			if droppedExpected > 0 && gotDropped == 0 && strings.TrimSpace(tc.in) != "" {
				// Re-count: empty lines don't count against droppedExpected.
				realDropped := 0
				for _, line := range strings.Split(strings.TrimRight(tc.in, "\n"), "\n") {
					if strings.TrimSpace(line) != "" && !looksLikeJSONRPC([]byte(line)) {
						realDropped++
					}
				}
				if realDropped != gotDropped {
					t.Errorf("errOut: expected %d dropped lines logged, got %d (errOut=%q)", realDropped, gotDropped, errOut.String())
				}
			}
		})
	}
}

// TestLooksLikeJSONRPC_PrescreenIsLoose is intentionally permissive:
// any object claiming jsonrpc=2.0 is allowed through to the SDK,
// which then returns proper error frames for bad method names,
// missing params, etc. The filter only catches "this can't possibly
// be JSON-RPC" — not "this is wrong JSON-RPC".
func TestLooksLikeJSONRPC_PrescreenIsLoose(t *testing.T) {
	pass := []string{
		`{"jsonrpc":"2.0","id":1,"method":"x"}`,
		`{"jsonrpc":"2.0","id":1,"method":"no/such/method"}`,           // bad method, SDK handles
		`{"jsonrpc":"2.0","id":1,"method":"x","extra_field":"ok"}`,    // unknown field, SDK handles
		`{"jsonrpc":"2.0","method":"notification"}`,                    // notification, no id, valid
		`{"jsonrpc":"2.0","result":{"value":1},"id":1}`,                // response shape
	}
	for _, in := range pass {
		if !looksLikeJSONRPC([]byte(in)) {
			t.Errorf("expected pass, got drop: %s", in)
		}
	}
	drop := []string{
		``,
		`not json`,
		`42`,
		`[]`,
		`null`,
		`{}`,
		`{"id":1,"method":"x"}`,        // no jsonrpc
		`{"jsonrpc":"1.0","id":1}`,     // wrong version
		`{"jsonrpc":2,"id":1}`,          // version is integer not string
	}
	for _, in := range drop {
		if looksLikeJSONRPC([]byte(in)) {
			t.Errorf("expected drop, got pass: %s", in)
		}
	}
}
