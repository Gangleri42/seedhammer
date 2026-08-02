package gui

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// deviceTags is what `tinygo info -target pico-plus2` reports. Omitting
// a tag can only pull in more files, which is the safe direction for a
// check that asserts a file is absent.
var deviceTags = []string{
	"cortexm", "baremetal", "linux", "arm", "rp2350", "rp", "rp2350b",
	"pico_plus2", "tinygo", "purego", "osusergo", "math_big_pure_go",
	"gc.conservative", "scheduler.tasks", "serial.usb", "tinygo.unicore",
}

// TestDeviceNeverImportsCryptoRand pins the reason the nil check in
// drawLastWordFlow is load-bearing.
//
// TinyGo picks what backs crypto/rand.Reader per chip, in its own
// src/crypto/rand/rand_baremetal.go, and that backend is machine.GetRNG:
// on rp2350 a ring-oscillator LFSR TinyGo documents as unfit for
// cryptography. rp2350 is absent from that build constraint today, so
// Reader is nil and a gui default of rand.Reader is nil too. Add rp2350
// to it in some later TinyGo and the same source silently links the LFSR
// as the seed entropy source, with no diff here and nothing failing.
//
// So do not depend on the value; depend on the import. If package gui
// does not import crypto/rand on this target, no code in gui can name
// crypto/rand.Reader, whatever a future toolchain decides it holds.
func TestDeviceNeverImportsCryptoRand(t *testing.T) {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH = "linux", "arm"
	ctx.BuildTags = deviceTags
	pkg, err := ctx.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(pkg.Imports, "crypto/rand") {
		t.Errorf("package gui imports crypto/rand on rp2350; the device seed source "+
			"must come from cmd/controller only.\nfiles in the device build: %v",
			pkg.GoFiles)
	}
}

// TestRandHasOneDeviceWriter enumerates every assignment to gui.Rand in
// the tree. The device source is installed in exactly one place, and a
// second writer is how a weak reader gets in without touching this
// package at all.
func TestRandHasOneDeviceWriter(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"gui/rand_default.go":            "rand.Reader",
		"cmd/controller/platform_sh2.go": "new(trng.Reader)",
	}
	got := map[string]string{}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == ".claude" || n == "deliverables" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		record := func(rhs ast.Expr) {
			var b strings.Builder
			printExpr(&b, rhs)
			got[rel] = b.String()
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range v.Lhs {
					if isRand(lhs) && i < len(v.Rhs) {
						record(v.Rhs[i])
					}
				}
			case *ast.ValueSpec:
				for i, name := range v.Names {
					if name.Name == "Rand" && f.Name.Name == "gui" && i < len(v.Values) {
						record(v.Values[i])
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for file, expr := range got {
		w, ok := want[file]
		if !ok {
			t.Errorf("unexpected writer of gui.Rand in %s: Rand = %s", file, expr)
			continue
		}
		if w != expr {
			t.Errorf("%s sets gui.Rand = %s, want %s", file, expr, w)
		}
	}
	for file := range want {
		if _, ok := got[file]; !ok {
			t.Errorf("expected a writer of gui.Rand in %s, found none", file)
		}
	}
}

func isRand(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "Rand"
	case *ast.SelectorExpr:
		p, ok := v.X.(*ast.Ident)
		return ok && p.Name == "gui" && v.Sel.Name == "Rand"
	}
	return false
}

func printExpr(b *strings.Builder, e ast.Expr) {
	switch v := e.(type) {
	case *ast.Ident:
		b.WriteString(v.Name)
	case *ast.SelectorExpr:
		printExpr(b, v.X)
		b.WriteString(".")
		b.WriteString(v.Sel.Name)
	case *ast.CallExpr:
		printExpr(b, v.Fun)
		b.WriteString("(")
		for i, a := range v.Args {
			if i > 0 {
				b.WriteString(", ")
			}
			printExpr(b, a)
		}
		b.WriteString(")")
	default:
		b.WriteString("?")
	}
}
