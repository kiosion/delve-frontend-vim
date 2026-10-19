package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRefusesDebugger(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("code signatures are checked on macOS only")
	}
	src, err := os.ReadFile(buildProgram(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	entitlements := func(value string) string {
		path := filepath.Join(dir, value+".plist")
		if err := os.WriteFile(path, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>com.apple.security.get-task-allow</key><`+value+`/></dict></plist>
`), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	for _, c := range []struct {
		name string
		sign []string
		want bool
	}{
		{"linker signature", nil, false},
		{"hardened runtime", []string{"-o", "runtime"}, true},
		{"hardened runtime with get-task-allow", []string{"-o", "runtime", "--entitlements", entitlements("true")}, false},
		{"hardened runtime with get-task-allow false", []string{"-o", "runtime", "--entitlements", entitlements("false")}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			exe := filepath.Join(t.TempDir(), "sleeper")
			if err := os.WriteFile(exe, src, 0o755); err != nil {
				t.Fatal(err)
			}
			if c.sign != nil {
				args := append([]string{"-s", "-", "-f"}, c.sign...)
				if out, err := exec.Command("codesign", append(args, exe)...).CombinedOutput(); err != nil {
					t.Fatalf("codesign: %v\n%s", err, out)
				}
			}
			if got := refusesDebugger(exe); got != c.want {
				t.Errorf("refusesDebugger = %v, want %v", got, c.want)
			}
		})
	}
}
