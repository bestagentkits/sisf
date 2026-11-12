package github_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/github"
)

const (
	modulePath = "github.com/bestagentkits/sisf"
	githubPkg  = modulePath + "/pkg/github"
)

// mergeNames are the names of pkg/github that make or describe a merge.
var mergeNames = map[string]bool{"NewMerger": true, "Merger": true, "MergeRequest": true, "Merged": true}

// listedPackage is what `go list` says of one package. IgnoredGoFiles are
// its files left out by build constraints on this platform: they are
// checked too, as another platform or tag would compile them.
type listedPackage struct {
	ImportPath     string
	Dir            string
	GoFiles        []string
	IgnoredGoFiles []string
}

// platforms are the systems whose build graphs are checked: a package
// imported only on one of them is in that one's graph.
var platforms = []string{"linux", "darwin", "windows"}

// buildGraph lists every package of this module compiled into the binary
// of cmd on any of platforms, from the build graph rather than from file
// names.
func buildGraph(t *testing.T, cmd string) []listedPackage {
	t.Helper()
	seen := map[string]bool{}
	var pkgs []listedPackage
	for _, goos := range platforms {
		list := exec.Command("go", "list", "-deps", "-json=ImportPath,Dir,GoFiles,IgnoredGoFiles", "./cmd/"+cmd)
		list.Dir = filepath.Join("..", "..")
		list.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
		out, err := list.Output()
		if err != nil {
			t.Fatalf("GOOS=%s go list -deps ./cmd/%s: %v", goos, cmd, err)
		}
		dec := json.NewDecoder(strings.NewReader(string(out)))
		for dec.More() {
			var p listedPackage
			if err := dec.Decode(&p); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(p.ImportPath, modulePath+"/") && p.ImportPath != githubPkg && !seen[p.ImportPath] {
				seen[p.ImportPath] = true
				pkgs = append(pkgs, p)
			}
		}
	}
	return pkgs
}

// mergeUses lists where the package's source names one of mergeNames of
// pkg/github, whatever name it imports pkg/github under. A dot import of
// pkg/github counts as a use: it would hide one.
func mergeUses(t *testing.T, p listedPackage) []string {
	t.Helper()
	var uses []string
	fset := token.NewFileSet()
	for _, name := range slices.Concat(p.GoFiles, p.IgnoredGoFiles) {
		file, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		local := ""
		for _, imp := range file.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == githubPkg {
				local = "github"
				if imp.Name != nil {
					local = imp.Name.Name
				}
			}
		}
		switch local {
		case "", "_":
			continue
		case ".":
			uses = append(uses, p.ImportPath+"/"+name+": dot import of pkg/github")
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == local && mergeNames[sel.Sel.Name] {
					uses = append(uses, fset.Position(sel.Pos()).String()+": "+local+"."+sel.Sel.Name)
				}
			}
			return true
		})
	}
	return uses
}

// Only the supervisor can merge into a default branch. Neither the client
// the agent and control zones are handed nor the REST client itself has a
// merge method, and no package compiled into the control and dispatcher
// binary (factoryd), the edge or the command-line tool names the merger,
// under any import name. The supervisor's binary does, which shows the
// check finds a use where there is one.
func TestOnlyTheSupervisorHasAWayToMerge(t *testing.T) {
	t.Parallel()
	for _, typ := range []reflect.Type{reflect.TypeFor[github.Client](), reflect.TypeFor[*github.RESTClient]()} {
		for i := range typ.NumMethod() {
			if name := typ.Method(i).Name; strings.Contains(strings.ToLower(name), "merge") {
				t.Errorf("%s has the method %s", typ, name)
			}
		}
	}
	for _, cmd := range []string{"factoryd", "edge", "sisf"} {
		for _, p := range buildGraph(t, cmd) {
			for _, use := range mergeUses(t, p) {
				t.Errorf("cmd/%s is built with %s, which reaches the merger: %s", cmd, p.ImportPath, use)
			}
		}
	}
	found := false
	for _, p := range buildGraph(t, "supervisor") {
		found = found || len(mergeUses(t, p)) > 0
	}
	if !found {
		t.Fatal("no package of the supervisor's binary names the merger: the check would find nothing anywhere")
	}
}

// mergeSegments are the path segments of GitHub's API that merge: a pull
// request's merge and a branch merge.
var mergeSegments = map[string]bool{"merge": true, "merges": true}

// Inside pkg/github itself, only the Merger's own methods name an API path
// that merges, and nothing but NewMerger builds a Merger. Any other way
// to merge, an exported wrapper of the REST client say, would hand the
// capability to every zone holding a client.
func TestOnlyTheMergerInsidePkgGithubReachesAMerge(t *testing.T) {
	t.Parallel()
	list := exec.Command("go", "list", "-json=ImportPath,Dir,GoFiles,IgnoredGoFiles", ".")
	out, err := list.Output()
	if err != nil {
		t.Fatal(err)
	}
	var p listedPackage
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	merges := 0
	for _, name := range slices.Concat(p.GoFiles, p.IgnoredGoFiles) {
		file, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, _ := decl.(*ast.FuncDecl)
			inMerger := fn != nil && fn.Recv != nil && receiverType(fn.Recv) == "Merger"
			builds := fn != nil && fn.Recv == nil && fn.Name.Name == "NewMerger"
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.BasicLit:
					if n.Kind != token.STRING {
						return true
					}
					value, _ := strconv.Unquote(n.Value)
					for segment := range strings.SplitSeq(value, "/") {
						if mergeSegments[segment] {
							if !inMerger {
								t.Errorf("%s: the API path %q is named outside the Merger", fset.Position(n.Pos()), value)
							}
							merges++
						}
					}
				case *ast.CompositeLit:
					if id, ok := n.Type.(*ast.Ident); ok && id.Name == "Merger" && !builds {
						t.Errorf("%s: a Merger is built outside NewMerger", fset.Position(n.Pos()))
					}
				case *ast.CallExpr:
					if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "NewMerger" {
						t.Errorf("%s: pkg/github itself calls NewMerger", fset.Position(n.Pos()))
					}
				}
				return true
			})
		}
	}
	if merges == 0 {
		t.Fatal("the Merger names no API path that merges: the check would find nothing anywhere")
	}
}

// receiverType is the type name of a method's receiver, without a pointer.
func receiverType(recv *ast.FieldList) string {
	if len(recv.List) == 0 {
		return ""
	}
	typ := recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
