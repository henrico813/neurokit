package internal

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var (
	// A bold label such as "**Ends.**" ends a sentence too.
	sentenceEnd = regexp.MustCompile(`[.!?]\**(\s|$)`)
	listItem    = regexp.MustCompile(`^(- |\d+\. )`)
)

// The README and pages under docs/ are read on phones, where long
// paragraphs, wide code lines and tables are hard to follow. The limits
// apply per line, so each paragraph or list item must stay on one line.
func TestDocsFitOnAPhone(t *testing.T) {
	checkPage := func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		inCode, prevProse := false, false
		for i, line := range strings.Split(string(text), "\n") {
			at := func(format string, args ...any) {
				t.Errorf("%s:%d: "+format, append([]any{path, i + 1}, args...)...)
			}
			isProse := !inCode && line != "" && !strings.HasPrefix(line, "```") &&
				!strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "|")
			sentences := len(sentenceEnd.FindAllString(line, -1))
			words := len(strings.Fields(line))
			switch {
			case strings.HasPrefix(line, "```"):
				inCode = !inCode
			case inCode && utf8.RuneCountInString(line) > 50:
				at("code line has %d characters; limit 50", utf8.RuneCountInString(line))
			case strings.HasPrefix(line, "|") && !inCode:
				at("table; use a list instead")
			case !isProse:
			case prevProse && !listItem.MatchString(line):
				at("paragraph continues from the line above; keep it on one line")
			case sentences > 3 || words > 60:
				at("paragraph has %d sentences and %d words; limit 3 and 60", sentences, words)
			}
			prevProse = isProse
		}
		return nil
	}
	for _, root := range []string{"../README.md", "../docs"} {
		if err := filepath.WalkDir(root, checkPage); err != nil {
			t.Fatal(err)
		}
	}
}
