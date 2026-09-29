package yur

import (
	"bytes"
	"container/heap"
	"sort"
	"sync"
)

// History: walking revs by their parents

// What a walk needs of a rev. Its generation is one more than its highest parent's, so a rev stands above
// all it comes from whatever the clocks said: a walk by generation knows when the rest can't matter.
type node struct {
	gen     uint32
	time    int64
	parents []Hash
}

// A rev never changes, so what is known of one holds for every store and for the life of the process
var graph = struct {
	sync.Mutex
	nodes map[Hash]*node
}{nodes: map[Hash]*node{}}

const graphLimit = 1 << 20

func knownNode(h Hash) *node {
	graph.Lock()
	defer graph.Unlock()
	return graph.nodes[h]
}

func rememberNode(h Hash, n *node) {
	graph.Lock()
	defer graph.Unlock()
	if len(graph.nodes) >= graphLimit {
		graph.nodes = map[Hash]*node{}
	}
	graph.nodes[h] = n
}

// The generations below a rev are worked out once for a history, then taken from memory
func nodeOf(s Store, h Hash) (*node, error) {
	if n := knownNode(h); n != nil {
		return n, nil
	}
	done := map[Hash]*node{}
	get := func(x Hash) *node {
		if n := done[x]; n != nil {
			return n
		}
		return knownNode(x)
	}
	read := map[Hash]*node{}
	stack := []Hash{h}
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		if get(top) != nil {
			stack = stack[:len(stack)-1]
			continue
		}
		n := read[top]
		if n == nil {
			r, err := ReadRev(s, top)
			if err != nil {
				return nil, err
			}
			n = &node{time: r.Time, parents: r.Parents}
			read[top] = n
		}
		ready, gen := true, uint32(0)
		for _, p := range n.parents {
			pn := get(p)
			if pn == nil {
				ready = false
				stack = append(stack, p)
			} else if pn.gen > gen {
				gen = pn.gen
			}
		}
		if ready {
			n.gen = gen + 1
			done[top] = n
			delete(read, top)
			rememberNode(top, n)
			stack = stack[:len(stack)-1]
		}
	}
	return done[h], nil
}

type walkItem struct {
	h Hash
	n *node
}

// Highest generation first, then latest
type walkQueue []walkItem

func (q walkQueue) Len() int { return len(q) }
func (q walkQueue) Less(i, j int) bool {
	a, b := q[i], q[j]
	if a.n.gen != b.n.gen {
		return a.n.gen > b.n.gen
	}
	if a.n.time != b.n.time {
		return a.n.time > b.n.time
	}
	return bytes.Compare(a.h[:], b.h[:]) < 0
}
func (q walkQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *walkQueue) Push(x interface{}) { *q = append(*q, x.(walkItem)) }
func (q *walkQueue) Pop() interface{} {
	old := *q
	it := old[len(old)-1]
	*q = old[:len(old)-1]
	return it
}

// A walk down from two sets of revs at once, each rev marked with the sides it is reached from. A rev
// leaves the queue only after every rev above it, so its marks are final by then.
type painter struct {
	s     Store
	marks map[Hash]uint8
	queue walkQueue
	// Revs in the queue by their marks
	waiting map[uint8]int
}

func newPainter(s Store) *painter {
	return &painter{s: s, marks: map[Hash]uint8{}, waiting: map[uint8]int{}}
}

func (p *painter) add(h Hash, mark uint8) error {
	if h.IsZero() {
		return nil
	}
	old, seen := p.marks[h]
	if seen {
		if old|mark != old {
			p.waiting[old]--
			p.waiting[old|mark]++
			p.marks[h] = old | mark
		}
		return nil
	}
	n, err := nodeOf(p.s, h)
	if err != nil {
		return err
	}
	p.marks[h] = mark
	p.waiting[mark]++
	heap.Push(&p.queue, walkItem{h, n})
	return nil
}

func (p *painter) next() (walkItem, uint8) {
	it := heap.Pop(&p.queue).(walkItem)
	mark := p.marks[it.h]
	p.waiting[mark]--
	return it, mark
}

const sideA, sideB, stale = 1, 2, 4

// The revs reached from a and not from b, and the other way round, highest first. Each list stops at
// limit (0: none); the second is not looked for unless both is set.
func split(s Store, a, b []Hash, limit int, both bool) ([]Hash, []Hash, error) {
	p := newPainter(s)
	for _, h := range a {
		if err := p.add(h, sideA); err != nil {
			return nil, nil, err
		}
	}
	for _, h := range b {
		if err := p.add(h, sideB); err != nil {
			return nil, nil, err
		}
	}
	onlyA, onlyB := []Hash{}, []Hash{}
	full := func(list []Hash) bool { return limit > 0 && len(list) >= limit }
	for p.queue.Len() > 0 {
		doneA := p.waiting[sideA] == 0 || full(onlyA)
		doneB := !both || p.waiting[sideB] == 0 || full(onlyB)
		if doneA && doneB {
			break
		}
		it, mark := p.next()
		switch {
		case mark == sideA && !full(onlyA):
			onlyA = append(onlyA, it.h)
		case mark == sideB && both && !full(onlyB):
			onlyB = append(onlyB, it.h)
		}
		for _, parent := range it.n.parents {
			if err := p.add(parent, mark); err != nil {
				return nil, nil, err
			}
		}
	}
	return onlyA, onlyB, nil
}

// The revs reached from the given ones and not from those in exclude, newest first by time, cut at limit
// (0: none)
func Only(s Store, from, exclude []Hash, limit int) ([]Hash, map[Hash]Rev, error) {
	order, _, err := split(s, from, exclude, limit, false)
	if err != nil {
		return nil, nil, err
	}
	revs := map[Hash]Rev{}
	for _, h := range order {
		r, err := ReadRev(s, h)
		if err != nil {
			return nil, nil, err
		}
		revs[h] = r
	}
	sort.SliceStable(order, func(i, j int) bool { return revs[order[i]].Time > revs[order[j]].Time })
	return order, revs, nil
}

// How many revs a has that b lacks, and b has that a lacks, each counted up to limit (0: all)
func AheadBehind(s Store, a, b Hash, limit int) (int, int, error) {
	ahead, behind, err := split(s, []Hash{a}, []Hash{b}, limit, true)
	return len(ahead), len(behind), err
}

// All the revs from the given ones down to (not into) those in stop, newest first by time: a limited
// walk gives the latest ones. Time is what people see; Topo gives an order parents come first in.
func Ancestors(s Store, from []Hash, stop map[Hash]bool, limit int) ([]Hash, map[Hash]Rev, error) {
	revs := map[Hash]Rev{}
	queue := &timeQueue{revs: revs}
	add := func(h Hash) error {
		if h.IsZero() || stop[h] {
			return nil
		}
		if _, seen := revs[h]; seen {
			return nil
		}
		r, err := ReadRev(s, h)
		if err != nil {
			return err
		}
		revs[h] = r
		heap.Push(queue, h)
		return nil
	}
	for _, h := range from {
		if err := add(h); err != nil {
			return nil, revs, err
		}
	}
	order := []Hash{}
	for queue.Len() > 0 {
		if limit > 0 && len(order) >= limit {
			break
		}
		h := heap.Pop(queue).(Hash)
		order = append(order, h)
		for _, p := range revs[h].Parents {
			if err := add(p); err != nil {
				return order, revs, err
			}
		}
	}
	return order, revs, nil
}

type timeQueue struct {
	list []Hash
	revs map[Hash]Rev
}

func (q timeQueue) Len() int { return len(q.list) }
func (q timeQueue) Less(i, j int) bool {
	a, b := q.revs[q.list[i]].Time, q.revs[q.list[j]].Time
	if a != b {
		return a > b
	}
	return bytes.Compare(q.list[i][:], q.list[j][:]) < 0
}
func (q timeQueue) Swap(i, j int)       { q.list[i], q.list[j] = q.list[j], q.list[i] }
func (q *timeQueue) Push(x interface{}) { q.list = append(q.list, x.(Hash)) }
func (q *timeQueue) Pop() interface{} {
	h := q.list[len(q.list)-1]
	q.list = q.list[:len(q.list)-1]
	return h
}

// The revs of a walk ordered so that a rev comes after its parents, whatever their times say: what a
// receiver of them or a git import needs. Parents outside the set are taken as already there.
func Topo(order []Hash, revs map[Hash]Rev) []Hash {
	in := map[Hash]bool{}
	for _, h := range order {
		in[h] = true
	}
	done := map[Hash]bool{}
	out := make([]Hash, 0, len(order))
	// Oldest first where the parents allow it, so the result stays close to time order
	for i := len(order) - 1; i >= 0; i-- {
		stack := []Hash{order[i]}
		for len(stack) > 0 {
			h := stack[len(stack)-1]
			if done[h] {
				stack = stack[:len(stack)-1]
				continue
			}
			waiting := false
			for _, p := range revs[h].Parents {
				if in[p] && !done[p] {
					stack = append(stack, p)
					waiting = true
				}
			}
			if !waiting {
				done[h] = true
				out = append(out, h)
				stack = stack[:len(stack)-1]
			}
		}
	}
	return out
}

// Only the revs above the ancestor are looked at: none below it can lead to it
func IsAncestor(s Store, ancestor, rev Hash) bool {
	if ancestor.IsZero() || ancestor == rev {
		return true
	}
	target, err := nodeOf(s, ancestor)
	if err != nil {
		return false
	}
	seen := map[Hash]bool{rev: true}
	queue := []Hash{rev}
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		n, err := nodeOf(s, h)
		if err != nil {
			return false
		}
		for _, p := range n.parents {
			if p == ancestor {
				return true
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			if pn, err := nodeOf(s, p); err == nil && pn.gen > target.gen {
				queue = append(queue, p)
			}
		}
	}
	return false
}

// The nearest rev both come from, zero when they share none. Found by the parents alone: a rev with a
// clock set wrong must not make an older base look nearer.
func MergeBase(s Store, a, b Hash) (Hash, error) {
	if a.IsZero() || b.IsZero() {
		return Zero, nil
	}
	p := newPainter(s)
	if err := p.add(a, sideA); err != nil {
		return Zero, err
	}
	if err := p.add(b, sideB); err != nil {
		return Zero, err
	}
	// A common rev met first has none of the others below it: what lies under it only goes stale
	found := []walkItem{}
	for p.waiting[sideA]+p.waiting[sideB]+p.waiting[sideA|sideB] > 0 {
		it, mark := p.next()
		if mark == sideA|sideB {
			found = append(found, it)
			mark |= stale
		}
		for _, parent := range it.n.parents {
			if err := p.add(parent, mark); err != nil {
				return Zero, err
			}
		}
	}
	if len(found) == 0 {
		return Zero, nil
	}
	// Criss-cross merges leave several; the latest of them is taken
	pick := found[0]
	for _, it := range found[1:] {
		if it.n.time > pick.n.time {
			pick = it
		}
	}
	return pick.h, nil
}
