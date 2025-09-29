package priority_queue_test

import (
	"testing"

	"github.com/buildbuddy-io/gin/internal/priority_queue"
	"github.com/stretchr/testify/assert"
)

type item struct {
	name     string
	priority int
}

func itemLessFn(i, j item) bool {
	// "lesser" items sort first, so if we want higher
	// priority items to pop first, they should sort as
	// less than lower priority items.
	return i.priority > j.priority
}

func TestPushPop(t *testing.T) {
	q := priority_queue.New[item](itemLessFn)
	q.Push(item{"A", 1})
	q.Push(item{"E", 5})
	q.Push(item{"D", 4})
	q.Push(item{"B", 2})

	v, ok := q.Pop()
	assert.Equal(t, "E", v.name)
	assert.True(t, ok)

	v, ok = q.Pop()
	assert.Equal(t, "D", v.name)
	assert.True(t, ok)

	v, ok = q.Pop()
	assert.Equal(t, "B", v.name)
	assert.True(t, ok)

	v, ok = q.Pop()
	assert.Equal(t, "A", v.name)
	assert.True(t, ok)

	v, ok = q.Pop()
	assert.Equal(t, "", v.name)
	assert.False(t, ok)
}
