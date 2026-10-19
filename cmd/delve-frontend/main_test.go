package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestServe(t *testing.T) {
	in := strings.Join([]string{
		`[1,{"method":"locate","params":{"path":"a_test.go","text":"package a\nimport \"testing\"\nfunc TestA(t *testing.T) {}\n"}}]`,
		`[2,{"method":"attach"}]`,
		`[3,{"method":"locate","params":{"path":"b_test.go","text":"package b\n"}}]`,
	}, "\n")
	var out bytes.Buffer
	if err := serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`[1,{"result":[{"name":"TestA","line":3,"end_line":3,"args":["-run","^TestA$"]}]}]`,
		`[2,{"result":null,"error":"unknown method \"attach\""}]`,
		`[3,{"result":[]}]`,
	}, "\n") + "\n"
	if got := out.String(); got != want {
		t.Errorf("responses differ\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestSplitArgs(t *testing.T) {
	for _, c := range []struct {
		line string
		want []string
	}{
		{`a  b`, []string{"a", "b"}},
		{`--name "a b" 'c d'`, []string{"--name", "a b", "c d"}},
		{`x"y z"w`, []string{"xy zw"}},
		{`'a\b' "a\"b\\c\d"`, []string{`a\b`, `a"b\c\d`}},
		{`a\ b ''`, []string{"a b", ""}},
	} {
		if got, err := splitArgs(c.line); err != nil || !slices.Equal(got, c.want) {
			t.Errorf("splitArgs(%q) = %q, %v, want %q", c.line, got, err, c.want)
		}
	}
	if _, err := splitArgs(`"open`); err == nil {
		t.Errorf("splitArgs accepts an unterminated quote")
	}
}
