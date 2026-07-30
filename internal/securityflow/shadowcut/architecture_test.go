package shadowcut

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestShadowPackageHasNoProductionDependencyOrApplyAPI(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller unavailable")
	}
	directory := filepath.Dir(testFile)
	files, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatal(err)
	}

	allowedImports := map[string]bool{
		"bytes":           true,
		"crypto/sha256":   true,
		"encoding/binary": true,
		"errors":          true,
		"math/bits":       true,
		"sort":            true,
	}
	forbiddenAPI := map[string]bool{
		"Apply":   true,
		"Persist": true,
		"Sign":    true,
		"Write":   true,
	}

	fileSet := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil || !allowedImports[value] {
				t.Fatalf("non-shadow dependency %q in %s", value, path)
			}
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Name.IsExported() &&
				forbiddenAPI[function.Name.Name] {
				t.Fatalf("forbidden API %s in %s", function.Name.Name, path)
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			field, ok := node.(*ast.Field)
			if !ok {
				return true
			}
			ast.Inspect(field.Type, func(typeNode ast.Node) bool {
				identifier, ok := typeNode.(*ast.Ident)
				if ok && identifier.Name == "string" {
					t.Fatalf("string field type in %s", path)
				}
				return true
			})
			return false
		})
	}

	productionDirectory := filepath.Dir(directory)
	productionFiles, err := filepath.Glob(
		filepath.Join(productionDirectory, "*.go"),
	)
	if err != nil {
		t.Fatal(err)
	}
	const shadowImport = "github.com/furukawa1020/conclution-ai-teacher/internal/securityflow/shadowcut"
	for _, path := range productionFiles {
		parsed, err := parser.ParseFile(
			fileSet,
			path,
			nil,
			parser.ImportsOnly,
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if value == shadowImport {
				t.Fatalf("production imports shadow package: %s", path)
			}
		}
	}
}
