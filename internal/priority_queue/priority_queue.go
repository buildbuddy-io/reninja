package priority_queue

import (
	"container/heap"
	"sync"
)

// Item is an element managed by a priority queue.
type Item[V any] struct {
	value V
	index int // The index of the item in the heap
}

func (i *Item[V]) Value() V {
	return i.value
}

func NewItem[V any](v V) *Item[V] {
	return &Item[V]{
		value: v,
	}
}

// PriorityQueue implements heap.Interface and holds items.
type PriorityQueue[V any] struct {
	items    []*Item[V]
	lessFunc func(i, j V) bool
}

func (pq PriorityQueue[V]) Len() int { return len(pq.items) }
func (pq PriorityQueue[V]) Less(i, j int) bool {
	// swap i and j because this is a priority queue not
	// a heap.
	return pq.lessFunc(pq.items[j].value, pq.items[i].value)
}
func (pq PriorityQueue[V]) Swap(i, j int) {
	pq.items[i], pq.items[j] = pq.items[j], pq.items[i]
	pq.items[i].index = i
	pq.items[j].index = j
}
func (pq *PriorityQueue[V]) Push(x any) {
	n := len(pq.items)
	item := x.(*Item[V])
	item.index = n
	pq.items = append(pq.items, item)
}
func (pq *PriorityQueue[V]) Pop() any {
	old := pq.items
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // for safety
	pq.items = old[0 : n-1]
	return item
}

func newPriorityQueue[V any](lessFunc func(i, j V) bool) PriorityQueue[V] {
	return PriorityQueue[V]{
		items:    make([]*Item[V], 0),
		lessFunc: lessFunc,
	}
}

// ThreadSafePriorityQueue implements a thread safe priority queue for type V.
// If the queue is empty, calling Pop() or Peek() will return a zero value of
// type V, or a specific empty value configured via options.
type ThreadSafePriorityQueue[V any] struct {
	mu    sync.Mutex // protects inner
	inner PriorityQueue[V]
}

func New[V any](lessFunc func(i, j V) bool) *ThreadSafePriorityQueue[V] {
	return &ThreadSafePriorityQueue[V]{
		mu:    sync.Mutex{},
		inner: newPriorityQueue[V](lessFunc),
	}
}

func (pq *ThreadSafePriorityQueue[V]) zeroValue() V {
	var zero V
	return zero
}

func (pq *ThreadSafePriorityQueue[V]) Push(v V) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	heap.Push(&pq.inner, NewItem(v))
}

func (pq *ThreadSafePriorityQueue[V]) Pop() (V, bool) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if len(pq.inner.items) == 0 {
		return pq.zeroValue(), false
	}
	item := heap.Pop(&pq.inner).(*Item[V])
	return item.value, true
}

func (pq *ThreadSafePriorityQueue[V]) Peek() (V, bool) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if len(pq.inner.items) == 0 {
		return pq.zeroValue(), false
	}
	item := pq.inner.items[0]
	return item.value, true
}

func (pq *ThreadSafePriorityQueue[V]) Len() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.inner.Len()
}
