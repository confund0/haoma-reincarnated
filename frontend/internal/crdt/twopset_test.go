package crdt

import (
	"reflect"
	"testing"
)

func TestAddContainsMembers(t *testing.T) {
	s := NewTwoPSet()
	s.Add("dev-a", 100)
	s.Add("dev-b", 101)

	if !s.Contains("dev-a") || !s.Contains("dev-b") {
		t.Fatal("added elements should be members")
	}
	if s.Contains("dev-x") {
		t.Fatal("never-added element must not be a member")
	}
	if got, want := s.Members(), []string{"dev-a", "dev-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Members = %v, want %v", got, want)
	}
}

func TestRemoveTombstones(t *testing.T) {
	s := NewTwoPSet()
	s.Add("dev-a", 100)
	s.Remove("dev-a", 200)

	if s.Contains("dev-a") {
		t.Fatal("removed element must not be a member")
	}
	if len(s.Members()) != 0 {
		t.Fatalf("Members should be empty, got %v", s.Members())
	}
}

func TestRemoveWinsPermanently(t *testing.T) {
	s := NewTwoPSet()
	s.Add("dev-a", 100)
	s.Remove("dev-a", 200)
	s.Add("dev-a", 300)

	if s.Contains("dev-a") {
		t.Fatal("a removed element must stay removed even after a later Add")
	}
}

func TestTimestampKeepsMaxAuditOnly(t *testing.T) {
	s := NewTwoPSet()
	s.Add("dev-a", 100)
	s.Add("dev-a", 50)
	if s.Added["dev-a"] != 100 {
		t.Fatalf("add ts = %d, want max 100", s.Added["dev-a"])
	}
	s.Add("dev-a", 150)
	if s.Added["dev-a"] != 150 {
		t.Fatalf("add ts = %d, want max 150", s.Added["dev-a"])
	}
}

func TestMergeUnionRemoveWins(t *testing.T) {
	a := NewTwoPSet()
	a.Add("dev-a", 100)
	a.Add("dev-b", 100)

	b := NewTwoPSet()
	b.Add("dev-c", 100)
	b.Remove("dev-b", 200)

	a.Merge(b)

	if got, want := a.Members(), []string{"dev-a", "dev-c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after merge Members = %v, want %v", got, want)
	}
	if a.Contains("dev-b") {
		t.Fatal("dev-b removed on b must be removed after merge into a")
	}
}

func TestMergeCommutative(t *testing.T) {
	build := func() (TwoPSet, TwoPSet) {
		a := NewTwoPSet()
		a.Add("dev-a", 100)
		a.Remove("dev-c", 150)
		b := NewTwoPSet()
		b.Add("dev-b", 120)
		b.Add("dev-c", 90)
		return a, b
	}

	a1, b1 := build()
	a1.Merge(b1)

	a2, b2 := build()
	b2.Merge(a2)

	if !reflect.DeepEqual(a1, b2) {
		t.Fatalf("merge not commutative:\n a∪b = %+v\n b∪a = %+v", a1, b2)
	}
}

func TestMergeIdempotent(t *testing.T) {
	a := NewTwoPSet()
	a.Add("dev-a", 100)
	a.Remove("dev-b", 200)

	snapshot := TwoPSet{
		Added:   map[string]int64{"dev-a": 100},
		Removed: map[string]int64{"dev-b": 200},
	}
	a.Merge(snapshot)
	a.Merge(snapshot)

	if !reflect.DeepEqual(a, snapshot) {
		t.Fatalf("merge not idempotent: got %+v, want %+v", a, snapshot)
	}
}

func TestMergeAssociative(t *testing.T) {
	mk := func() (TwoPSet, TwoPSet, TwoPSet) {
		a := NewTwoPSet()
		a.Add("dev-a", 100)
		b := NewTwoPSet()
		b.Add("dev-b", 110)
		b.Remove("dev-a", 130)
		c := NewTwoPSet()
		c.Add("dev-c", 120)
		return a, b, c
	}

	a1, b1, c1 := mk()
	a1.Merge(b1)
	a1.Merge(c1)

	a2, b2, c2 := mk()
	b2.Merge(c2)
	a2.Merge(b2)

	if !reflect.DeepEqual(a1, a2) {
		t.Fatalf("merge not associative:\n (a∪b)∪c = %+v\n a∪(b∪c) = %+v", a1, a2)
	}
}

func TestZeroValueTolerated(t *testing.T) {
	var s TwoPSet
	if s.Contains("x") {
		t.Fatal("nil-map set contains nothing")
	}
	if len(s.Members()) != 0 {
		t.Fatal("nil-map set has no members")
	}
	s.Add("dev-a", 100)
	if !s.Contains("dev-a") {
		t.Fatal("Add on nil-map set should work")
	}

	var empty TwoPSet
	s.Merge(empty)
	if !s.Contains("dev-a") {
		t.Fatal("merge with empty must preserve members")
	}
}
