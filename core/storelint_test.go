package gocommerce

import (
	"bufio"
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// These two tests read the source rather than run it. Row security already
// hides other stores' rows from a statement that forgot its filter; this is
// the first layer, the one kitcommerce-style code is reviewed against: every
// statement on a store table names store_id.

var createTableRE = regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?([a-z_]+)`)

// lintStoreTables is core's store tables plus every table a module creates.
func lintStoreTables() []string {
	tables := slices.Clone(coreStoreTables)
	_ = filepath.WalkDir("../ext", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, m := range createTableRE.FindAllStringSubmatch(string(b), -1) {
			if !slices.Contains(tables, m[1]) {
				tables = append(tables, m[1])
			}
		}
		return nil
	})
	return tables
}

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

func lintFuncName(fd *ast.FuncDecl) string {
	if fd.Recv != nil && len(fd.Recv.List) == 1 {
		typ := fd.Recv.List[0].Type
		if se, ok := typ.(*ast.StarExpr); ok {
			typ = se.X
		}
		if ix, ok := typ.(*ast.IndexExpr); ok {
			typ = ix.X
		}
		if id, ok := typ.(*ast.Ident); ok {
			return id.Name + "." + fd.Name.Name
		}
	}
	return fd.Name.Name
}

const storelintPending = "testdata/storelint_pending.txt"

func TestStatementsOnStoreTablesNameTheStore(t *testing.T) {
	dml := regexp.MustCompile(`(?is)\b(from|join|update|into|delete\s+from)\s+(?:only\s+)?(` +
		strings.Join(lintStoreTables(), "|") + `)\b`)
	pending := map[string]bool{}
	if f, err := os.Open(storelintPending); err == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			if line := strings.TrimSpace(s.Text()); line != "" {
				pending[line] = true
			}
		}
		f.Close()
	}
	var found []string
	for _, path := range lintFiles() {
		fset := gotoken.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fd.Doc != nil && strings.Contains(fd.Doc.Text(), "storelint:global") {
				continue
			}
			var hit, named bool
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == gotoken.STRING {
					if dml.MatchString(lit.Value) {
						hit = true
					}
					if strings.Contains(lit.Value, "store_id") {
						named = true
					}
				}
				return true
			})
			if hit && !named {
				found = append(found, path+":"+lintFuncName(fd))
			}
		}
	}
	sort.Strings(found)
	found = slices.Compact(found)
	if os.Getenv("STORELINT_WRITE") == "1" {
		if err := os.WriteFile(storelintPending, []byte(strings.Join(found, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, f := range found {
		if !pending[f] {
			t.Errorf("%s runs SQL on a store table without naming store_id", f)
		}
	}
	for f := range pending {
		if !slices.Contains(found, f) {
			t.Errorf("%s is listed as pending but is clean now: remove it from %s", f, storelintPending)
		}
	}
}

// contextFreeCall matches database calls that take no context. Such a call
// runs with context.Background, unbound, and sees no store's rows.
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
