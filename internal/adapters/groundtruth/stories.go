package groundtruth

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// Story is one entry from stories.md. Each story has a canonical
// short version (≤40 words is the convention but not enforced), a
// long version, an anti-drift bullet list, and a sensitivity flag.
//
// Line is the 1-based line number where the "## STORY: <name>" header
// appears in the source file. Used for error messages when a story
// fails validation in later issues.
type Story struct {
	Name        string
	Short       string
	Long        string
	DoNotDrift  []string
	Sensitivity string
	Line        int
}

// Stories is the parsed stories.md content, keyed by story name
// (case-folded to lowercase for case-insensitive lookup). The
// original name is preserved on the Story value.
type Stories map[string]Story

// loadStories reads path and parses it into a Stories map. A missing
// file returns an empty map (not an error).
//
// Parser shape (deliberately permissive — real-world stories.md will
// have variations):
//
//   - A story begins on a line matching "## STORY: <name>".
//   - Inside a story, "### Short version" / "### Long version" /
//     "### Do NOT drift" / "### Sensitivity" subsections collect
//     body text until the next "###" or "##".
//   - Lines before the first story are ignored (operator preamble).
//   - "Do NOT drift" bullets are any line starting with "- " inside
//     that subsection.
//   - Sensitivity captures the first non-blank line of its subsection
//     as a string ("public-safe", "internal", "private").
func loadStories(path string) (Stories, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Stories{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	return parseStories(path, f)
}

func parseStories(path string, r io.Reader) (Stories, error) {
	out := Stories{}
	sc := bufio.NewScanner(r)
	// stories.md sections can hold paragraph-length text; bump
	// MaxScanTokenSize so a long paragraph doesn't tip the scanner
	// into bufio.ErrTooLong.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		current   *Story
		subsec    string // "short" | "long" | "donotdrift" | "sensitivity" | ""
		shortBuf  strings.Builder
		longBuf   strings.Builder
		sensBuf   strings.Builder
		bullets   []string
		lineNum   int
		storyLine int
	)

	commit := func() {
		if current == nil {
			return
		}
		current.Short = strings.TrimSpace(shortBuf.String())
		current.Long = strings.TrimSpace(longBuf.String())
		current.Sensitivity = strings.TrimSpace(sensBuf.String())
		current.DoNotDrift = append([]string(nil), bullets...)
		current.Line = storyLine
		out[strings.ToLower(current.Name)] = *current
		current = nil
		subsec = ""
		shortBuf.Reset()
		longBuf.Reset()
		sensBuf.Reset()
		bullets = nil
	}

	for sc.Scan() {
		lineNum++
		line := sc.Text()
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "## "):
			// Any ## heading begins a story. v0.6 required the
			// "## STORY:" prefix; v0.53 (fix/stories-loose-heading)
			// relaxes the rule so operators can use their existing
			// markdown style. The "STORY:" prefix is still accepted
			// for backwards compat — it gets stripped from the name.
			commit()
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			name = strings.TrimSpace(strings.TrimPrefix(name, "STORY:"))
			if name == "" {
				return nil, fmt.Errorf("%s:%d: empty story name", path, lineNum)
			}
			current = &Story{Name: name}
			storyLine = lineNum

		case current == nil:
			// Preamble lines before the first story header — ignored.

		case strings.HasPrefix(trimmed, "### "):
			header := strings.ToLower(strings.TrimPrefix(trimmed, "### "))
			switch {
			case strings.HasPrefix(header, "short version"):
				subsec = "short"
			case strings.HasPrefix(header, "long version"):
				subsec = "long"
			case strings.HasPrefix(header, "do not drift"),
				strings.HasPrefix(header, "do-not-drift"),
				strings.HasPrefix(header, "do not"):
				subsec = "donotdrift"
			case strings.HasPrefix(header, "sensitivity"):
				subsec = "sensitivity"
			default:
				subsec = ""
			}

		default:
			switch subsec {
			case "short":
				shortBuf.WriteString(line)
				shortBuf.WriteByte('\n')
			case "long":
				longBuf.WriteString(line)
				longBuf.WriteByte('\n')
			case "donotdrift":
				if strings.HasPrefix(trimmed, "- ") {
					bullets = append(bullets, strings.TrimSpace(strings.TrimPrefix(trimmed, "-")))
				}
			case "sensitivity":
				if trimmed != "" && sensBuf.Len() == 0 {
					sensBuf.WriteString(trimmed)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	commit()
	return out, nil
}

// Get returns the story with the given name (case-insensitive). The
// second return value reports whether the story exists.
func (s Stories) Get(name string) (Story, bool) {
	v, ok := s[strings.ToLower(name)]
	return v, ok
}
