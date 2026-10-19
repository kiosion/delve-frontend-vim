package locate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const fixture = "testdata/fixture/fixture_test.go"

var wantComment = regexp.MustCompile(`// want: (\S+)`)

func TestFile(t *testing.T) {
	src, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for i, line := range strings.Split(string(src), "\n") {
		if m := wantComment.FindStringSubmatch(line); m != nil {
			want = append(want, fmt.Sprintf("%d %s", i+1, m[1]))
		}
	}

	tests, err := File(fixture, src)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, test := range tests {
		got = append(got, fmt.Sprintf("%d %s", test.Line, test.Name))
	}
	for _, g := range got {
		if !slices.Contains(want, g) {
			t.Errorf("unexpected test: %s", g)
		}
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing test: %s", w)
		}
	}
}

func TestFileArgsSelectTest(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the fixture package")
	}
	binary := filepath.Join(t.TempDir(), "fixture.test")
	// The fixture holds names that vet rejects, such as Testable.
	build := exec.Command("go", "test", "-c", "-vet=off", "-o", binary, ".")
	build.Dir = filepath.Dir(fixture)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	src, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	tests, err := File(fixture, src)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			args := []string{"-test.v", "-test.benchtime", "1x"}
			for _, arg := range test.Args {
				if strings.HasPrefix(arg, "-") {
					arg = "-test." + arg[1:]
				}
				args = append(args, arg)
			}
			out, _ := exec.Command(binary, args...).CombinedOutput()
			ran := ranTests(string(out))
			if len(ran) == 0 {
				t.Fatalf("%v ran no tests\n%s", test.Args, out)
			}
			for _, name := range ran {
				related := name == test.Name ||
					strings.HasPrefix(test.Name, name+"/") ||
					strings.HasPrefix(name, test.Name+"/")
				if !related {
					t.Errorf("%v also ran %s", test.Args, name)
				}
			}
			if !slices.Contains(ran, test.Name) {
				t.Errorf("%v did not run %s\n%s", test.Args, test.Name, out)
			}
		})
	}
}

var (
	runLine   = regexp.MustCompile(`(?m)^=== RUN\s+(\S+)$`)
	benchLine = regexp.MustCompile(`(?m)^(Benchmark\S*?)(?:-\d+)?(?:\s.*)?$`)
)

func ranTests(out string) []string {
	var names []string
	for _, m := range runLine.FindAllStringSubmatch(out, -1) {
		names = append(names, m[1])
	}
	for _, m := range benchLine.FindAllStringSubmatch(out, -1) {
		names = append(names, m[1])
	}
	return names
}
