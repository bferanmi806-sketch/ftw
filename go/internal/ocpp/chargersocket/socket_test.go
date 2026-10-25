package chargersocket

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lorenzodonini/ocpp-go/ws"
)

// The write pump reports a server-side close to the handler of the moment.
// When ocppj.Client.Stop installs its own handler after that, Stop must
// still hear of the disconnect, once, or it waits forever.
func TestSocketPassesAMissedDisconnectToStop(t *testing.T) {
	s := New(ws.NewClient())
	var first, second int
	s.SetDisconnectedHandler(func(error) { first++ })
	s.disconnected(errors.New("server closed")) // as the write pump does
	s.SetDisconnectedHandler(func(error) { second++ })
	s.Stop()
	s.Stop()
	if first != 1 || second != 1 {
		t.Fatalf("disconnect reached the first handler %d times and Stop's %d times, want once each", first, second)
	}
	if err := s.Write([]byte("[]")); err == nil {
		t.Fatal("a stopped charger still wrote")
	}
}

// Every charger in the module dials through a Socket. The races above need
// a few instructions of bad luck, so a charger built on the plain client
// passes its own tests and fails CI weeks later; this finds it at once.
func TestEveryChargerDialsThroughASocket(t *testing.T) {
	root := moduleRoot(t)
	clients := map[string]string{
		"github.com/lorenzodonini/ocpp-go/ocpp1.6":   "NewChargePoint",
		"github.com/lorenzodonini/ocpp-go/ocpp2.0.1": "NewChargingStation",
	}
	here, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == here || d.Name() == "testdata" || d.Name() == "vendor" || (path != root && strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		names := map[string]string{} // local package name -> constructor
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			ctor, ok := clients[p]
			if !ok {
				continue
			}
			name := filepath.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			names[name] = ctor
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && names[pkg.Name] == sel.Sel.Name {
				rel, _ := filepath.Rel(root, path)
				found = append(found, rel+":"+strconv.Itoa(fset.Position(sel.Pos()).Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("chargers built on ocpp-go's plain client, use ChargePoint16 or ChargingStation201:\n%s", strings.Join(found, "\n"))
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above this package")
		}
		dir = parent
	}
}
