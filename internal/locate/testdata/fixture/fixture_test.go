package fixture

import (
	"fmt"
	"testing"
)

type testCase struct {
	desc string
	want int
}

var packageCases = []testCase{
	{desc: "package level", want: 1}, // want: TestPackageTable/package_level
}

func TestLiteral(t *testing.T) { // want: TestLiteral
	t.Run("first case", func(t *testing.T) { // want: TestLiteral/first_case
		t.Run("inner", func(t *testing.T) {}) // want: TestLiteral/first_case/inner
	})
}

func TestSlice(t *testing.T) { // want: TestSlice
	tests := []struct {
		name string
		in   int
	}{
		{name: "empty input", in: 0}, // want: TestSlice/empty_input
		{"positional", 1},            // want: TestSlice/positional
		{ // want: TestSlice/multi_line
			name: "multi line",
			in:   2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {})
	}
}

func TestNamedType(t *testing.T) { // want: TestNamedType
	cases := []testCase{
		{desc: "named", want: 1}, // want: TestNamedType/named
		{"named positional", 2},  // want: TestNamedType/named_positional
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {})
	}
}

func TestPointer(t *testing.T) { // want: TestPointer
	var cases = []*testCase{
		{desc: "elided"},           // want: TestPointer/elided
		&testCase{desc: "address"}, // want: TestPointer/address
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {})
	}
}

func TestMap(t *testing.T) { // want: TestMap
	for name, tc := range map[string]struct{ in int }{
		"map key": {in: 1}, // want: TestMap/map_key
	} {
		_ = tc
		t.Run(name, func(t *testing.T) {})
	}
}

func TestStrings(t *testing.T) { // want: TestStrings
	for _, s := range []string{
		"alpha", // want: TestStrings/alpha
		"beta",  // want: TestStrings/beta
	} {
		t.Run(s, func(t *testing.T) {})
	}
}

func TestPackageTable(t *testing.T) { // want: TestPackageTable
	for _, tc := range packageCases {
		t.Run(tc.desc, func(t *testing.T) {})
	}
}

func TestSiblingType(t *testing.T) { // want: TestSiblingType
	cases := []siblingCase{
		{"sibling"}, // want: TestSiblingType/sibling
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {})
	}
}

func TestDuplicate(t *testing.T) { // want: TestDuplicate
	for _, s := range []string{
		"same", // want: TestDuplicate/same
		"same", // want: TestDuplicate/same#01
	} {
		t.Run(s, func(t *testing.T) {})
	}
}

func TestSpecial(t *testing.T) { // want: TestSpecial
	t.Run("a/b (c)", func(t *testing.T) {}) // want: TestSpecial/a/b_(c)
	t.Run("x"+"y", func(t *testing.T) {})   // want: TestSpecial/xy
}

func TestGroup(t *testing.T) { // want: TestGroup
	outer := []string{
		"outer case", // want: TestGroup/group/outer_case
	}
	t.Run("group", func(t *testing.T) { // want: TestGroup/group
		for _, s := range outer {
			t.Run(s, func(t *testing.T) {})
		}
	})
}

func TestComputed(t *testing.T) { // want: TestComputed
	for i := range 2 {
		t.Run(fmt.Sprintf("case %d", i), func(t *testing.T) {})
	}
}

func Testable(t *testing.T) {}

func BenchmarkSum(b *testing.B) { // want: BenchmarkSum
	b.Run("small", func(b *testing.B) { // want: BenchmarkSum/small
		for b.Loop() {
		}
	})
}

func FuzzParse(f *testing.F) { // want: FuzzParse
	f.Add("seed")
	f.Fuzz(func(t *testing.T, s string) {})
}

func ExampleHello() { // want: ExampleHello
	fmt.Println("hello")
	// Output: hello
}

func ExampleCompiledOnly() {
	fmt.Println("not run")
}
