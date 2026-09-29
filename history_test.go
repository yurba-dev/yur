package yur

import (
	"math/rand"
	"testing"
)

// Every walk by generation must agree with the plain one over whole ancestor sets, on random histories
// with clocks that go wherever they like
func TestHistoryWalks(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	root := tree(t, s, map[string]string{"f": "1"})
	rng := rand.New(rand.NewSource(7))
	all := []Hash{}
	for i := 0; i < 120; i++ {
		parents := []Hash{}
		if len(all) > 0 && rng.Intn(12) != 0 {
			parents = append(parents, all[len(all)-1-rng.Intn(min(len(all), 6))])
			if rng.Intn(4) == 0 {
				other := all[rng.Intn(len(all))]
				if other != parents[0] {
					parents = append(parents, other)
				}
			}
		}
		h, err := WriteRev(s, Rev{Tree: root, Parents: parents, Change: NewChange(), Time: rng.Int63n(1000), Message: "m"})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, h)
	}
	below := func(h Hash) map[Hash]bool {
		order, _, err := Ancestors(s, []Hash{h}, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		set := map[Hash]bool{}
		for _, x := range order {
			set[x] = true
		}
		return set
	}
	for round := 0; round < 300; round++ {
		a, b := all[rng.Intn(len(all))], all[rng.Intn(len(all))]
		ofA, ofB := below(a), below(b)
		only := 0
		for h := range ofA {
			if !ofB[h] {
				only++
			}
		}
		other := 0
		for h := range ofB {
			if !ofA[h] {
				other++
			}
		}
		ahead, behind, err := AheadBehind(s, a, b, 0)
		if err != nil || ahead != only || behind != other {
			t.Fatalf("ahead/behind %d/%d, want %d/%d (%v)", ahead, behind, only, other, err)
		}
		list, _, _ := Only(s, []Hash{a}, []Hash{b}, 0)
		for _, h := range list {
			if !ofA[h] || ofB[h] {
				t.Fatalf("%s is not only in a", h.Short())
			}
		}
		if len(list) != only {
			t.Fatalf("only gave %d, want %d", len(list), only)
		}
		if IsAncestor(s, b, a) != ofA[b] {
			t.Fatalf("is %s below %s", b.Short(), a.Short())
		}
		base, err := MergeBase(s, a, b)
		if err != nil {
			t.Fatal(err)
		}
		if base.IsZero() {
			for h := range ofA {
				if ofB[h] {
					t.Fatal("no base found for histories that meet")
				}
			}
			continue
		}
		if !ofA[base] || !ofB[base] {
			t.Fatal("the base is not in both")
		}
		// No common rev may stand above the base
		for h := range ofA {
			if ofB[h] && h != base && below(h)[base] {
				t.Fatalf("%s is common and nearer than %s", h.Short(), base.Short())
			}
		}
	}
}
