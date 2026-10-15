package gocommerce

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This test reads the source rather than runs it. Every statement is bound
// to its store through its context (storedriver.go), so a call that takes
// none runs unbound and, under `serve`, sees no store's rows at all.

func lintFiles() []string {
	var files []string
	for _, root := range []string{".", "../ext"} {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, filepath.ToSlash(p))
			}
			return nil
		})
	}
	return files
}

// contextFreeCall matches database calls that take no context.
var contextFreeCall = regexp.MustCompile(`\b(db|tx|DB\(\)|conn|stmt|insert|q)\.(Query|QueryRow|Exec|Begin|Prepare)\(`)

func TestDatabaseCallsTakeAContext(t *testing.T) {
	for _, path := range lintFiles() {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if contextFreeCall.MatchString(line) {
				t.Errorf("%s:%d: a database call without a context runs unbound: %s", path, i+1, strings.TrimSpace(line))
			}
		}
	}
}
