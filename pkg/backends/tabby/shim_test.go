package tabby_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llamactl/pkg/backends/tabby"
)

func TestPrependPythonPath(t *testing.T) {
	sep := string(os.PathListSeparator)

	if got := tabby.PrependPythonPath("", `C:\shim`); got != `C:\shim` {
		t.Fatalf("empty existing: got %q", got)
	}
	if got := tabby.PrependPythonPath(`C:\other`, ""); got != `C:\other` {
		t.Fatalf("empty dir: got %q", got)
	}
	got := tabby.PrependPythonPath(`C:\other`+sep+`D:\x`, `E:\shim`)
	want := `E:\shim` + sep + `C:\other` + sep + `D:\x`
	if got != want {
		t.Fatalf("prepend: got %q want %q", got, want)
	}
}

func TestEnsureShimDir_ExtractsSitecustomize(t *testing.T) {
	dir, err := tabby.EnsureShimDir()
	if err != nil {
		t.Fatalf("EnsureShimDir: %v", err)
	}
	if dir == "" {
		t.Fatal("expected non-empty shim dir")
	}
	site := filepath.Join(dir, "sitecustomize.py")
	data, err := os.ReadFile(site)
	if err != nil {
		t.Fatalf("read sitecustomize.py: %v", err)
	}
	body := string(data)
	for _, needle := range []string{"/slots", "status_display", "n_decoded", "setup_app"} {
		if !strings.Contains(body, needle) {
			t.Errorf("sitecustomize.py missing %q", needle)
		}
	}
}

func TestInjectPythonPath(t *testing.T) {
	env := map[string]string{"TABBYAPI_LLAMACTL_TIMING": "1"}
	tabby.InjectPythonPath(env)

	pp := env["PYTHONPATH"]
	if pp == "" {
		t.Fatal("expected PYTHONPATH to be set")
	}
	dir, err := tabby.EnsureShimDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pp, dir) {
		t.Fatalf("PYTHONPATH %q should start with shim dir %q", pp, dir)
	}
	if _, err := os.Stat(filepath.Join(strings.Split(pp, string(os.PathListSeparator))[0], "sitecustomize.py")); err != nil {
		t.Fatalf("sitecustomize.py not reachable via PYTHONPATH: %v", err)
	}
}
