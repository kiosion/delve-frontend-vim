package fixture

import (
	"context"
	"os"
	"runtime/pprof"
	"testing"
)

type point struct{ x, y int }

type shape struct {
	name   string
	corner *point
	none   *point
	tags   map[string]int
	points []point
	value  any
}

func TestShape(t *testing.T) {
	s := shape{
		name:   "square",
		corner: &point{1, 2},
		tags:   map[string]int{"sides": 4},
		points: []point{{0, 0}, {1, 1}},
		value:  point{3, 4},
	}
	n := 1
	{
		n := n + 1 // shadow
		t.Log(n)   // inner
	}
	env := os.Getenv("FIXTURE_ENV")
	done := make(chan struct{})
	defer close(done)
	started := make(chan struct{})
	for range 2 {
		go pprof.Do(context.Background(), pprof.Labels("worker", "waiter"), func(context.Context) {
			started <- struct{}{}
			<-done // waiting
		})
	}
	<-started
	<-started
	for i := range 3 {
		t.Log(s.name, i, env, tag) // breakpoint
	}
}
