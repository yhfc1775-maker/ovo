//go:build ignore

// Reproduce the bounded mechanism census described in REAUDIT.md.
// Usage: go run scripts/audit-remediation-census.go ROOT [BASE_REF]
package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Site struct {
	File          string `json:"file"`
	Function      string `json:"function"`
	Line          int    `json:"line"`
	Mechanism     string `json:"mechanism"`
	Unchecked     bool   `json:"unchecked,omitempty"`
	MissingPolicy bool   `json:"missingPolicy,omitempty"`
}

func main() {
	root := os.Args[1]
	ref := ""
	if len(os.Args) > 2 {
		ref = os.Args[2]
	}
	roots := []string{"go-backend/internal/http/handler", "go-backend/internal/store/repo", "go-gost/x/api", "go-gost/x/socket", "go-gost/x/config/loader", "go-gost/x/config/parsing"}
	var paths []string
	for _, dir := range roots {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") && !strings.HasSuffix(p, ".pb.go") {
				rel, _ := filepath.Rel(root, p)
				paths = append(paths, rel)
			}
			return nil
		})
	}
	sort.Strings(paths)
	var sites []Site
	selected := map[string]bool{"OnUpdate": true, "commitNamedConfig": true, "MutateServices": true, "ParseService": true, "ParseChain": true, "ParseHop": true, "ParseNode": true, "NewTransport": true, "NewHop": true, "NewChain": true, "VerifyPassword": true, "UpdatePasskeyCredential": true, "UpdateUserNameAndPasswordIfCurrent": true, "CreatePasskeyIfCurrent": true, "DeletePasskeyAndRevokeSessionsIfCurrent": true, "FinishPasskeyLogin": true, "DefaultSubscription": true, "GetSubscriptionByToken": true}
	for _, p := range paths {
		var src []byte
		var err error
		if ref != "" {
			c := exec.Command("git", "show", ref+":"+p)
			c.Dir = root
			src, err = c.Output()
			if err != nil {
				continue
			}
		} else {
			src, err = os.ReadFile(filepath.Join(root, p))
			if err != nil {
				panic(err)
			}
		}
		fs := token.NewFileSet()
		file, err := parser.ParseFile(fs, p, src, 0)
		if err != nil {
			panic(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ignored := map[*ast.CallExpr]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.ExprStmt:
					if call, ok := v.X.(*ast.CallExpr); ok {
						ignored[call] = true
					}
				case *ast.AssignStmt:
					if len(v.Rhs) == 1 && len(v.Lhs) == 1 {
						if id, ok := v.Lhs[0].(*ast.Ident); ok && id.Name == "_" {
							if call, ok := v.Rhs[0].(*ast.CallExpr); ok {
								ignored[call] = true
							}
						}
					}
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					var name string
					switch f := call.Fun.(type) {
					case *ast.Ident:
						name = f.Name
					case *ast.SelectorExpr:
						name = f.Sel.Name
					}
					if selected[name] {
						sites = append(sites, Site{File: p, Function: fn.Name.Name, Line: fs.Position(call.Pos()).Line, Mechanism: name, Unchecked: name == "OnUpdate" && ignored[call]})
					}
				}
				if lit, ok := n.(*ast.CompositeLit); ok {
					if typ, ok := lit.Type.(*ast.SelectorExpr); ok && typ.Sel.Name == "ForwardRecord" {
						hasRemote, hasPolicy := false, false
						for _, elt := range lit.Elts {
							if kv, ok := elt.(*ast.KeyValueExpr); ok {
								if id, ok := kv.Key.(*ast.Ident); ok {
									hasRemote = hasRemote || id.Name == "RemoteAddr"
									hasPolicy = hasPolicy || id.Name == "TargetPolicy"
								}
							}
						}
						if hasRemote {
							sites = append(sites, Site{File: p, Function: fn.Name.Name, Line: fs.Position(lit.Pos()).Line, Mechanism: "ForwardRecordAdapter", MissingPolicy: !hasPolicy})
						}
					}
				}
				return true
			})
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sites); err != nil {
		panic(err)
	}
}
