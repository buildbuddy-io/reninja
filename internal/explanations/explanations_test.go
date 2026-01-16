package explanations_test

import (
	"testing"

	"github.com/buildbuddy-io/reninja/internal/explanations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type item struct {
	i int
}

func makeItem(i int) item {
	return item{i}
}

func TestExplanations(t *testing.T) {
	exp := explanations.New()

	exp.Record(makeItem(1), "first explanation")
	exp.Record(makeItem(1), "second explanation")
	exp.Record(makeItem(2), "third explanation")
	exp.Record(makeItem(2), "fourth %s", "explanation")

	list := exp.LookupAndAppend(makeItem(0))
	require.Empty(t, list)

	list = exp.LookupAndAppend(makeItem(1))
	require.Equal(t, 2, len(list))
	assert.Equal(t, "first explanation", list[0])
	assert.Equal(t, "second explanation", list[1])

	list = append(list, exp.LookupAndAppend(makeItem(2))...)
	require.Equal(t, 4, len(list))
	assert.Equal(t, "first explanation", list[0])
	assert.Equal(t, "second explanation", list[1])
	assert.Equal(t, "third explanation", list[2])
	assert.Equal(t, "fourth explanation", list[3])
}

func TestOptionalExplanationsNonNull(t *testing.T) {
	parent := explanations.New()
	exp := explanations.NewOptional(parent)

	exp.Record(makeItem(1), "first explanation")
	exp.Record(makeItem(1), "second explanation")
	exp.Record(makeItem(2), "third explanation")
	exp.Record(makeItem(2), "fourth %s", "explanation")

	list := exp.LookupAndAppend(makeItem(0))
	require.Empty(t, list)

	list = exp.LookupAndAppend(makeItem(1))
	require.Equal(t, 2, len(list))
	assert.Equal(t, "first explanation", list[0])
	assert.Equal(t, "second explanation", list[1])

	list = append(list, exp.LookupAndAppend(makeItem(2))...)
	require.Equal(t, 4, len(list))
	assert.Equal(t, "first explanation", list[0])
	assert.Equal(t, "second explanation", list[1])
	assert.Equal(t, "third explanation", list[2])
	assert.Equal(t, "fourth explanation", list[3])
}

func TestOptionalExplanationsWithNullPointer(t *testing.T) {
	exp := explanations.NewOptional(nil)

	exp.Record(makeItem(1), "first explanation")
	exp.Record(makeItem(1), "second explanation")
	exp.Record(makeItem(2), "third explanation")
	exp.Record(makeItem(2), "fourth %s", "explanation")

	list := exp.LookupAndAppend(makeItem(0))
	require.Empty(t, list)

	list = exp.LookupAndAppend(makeItem(1))
	require.Empty(t, list)

	list = exp.LookupAndAppend(makeItem(2))
	require.Empty(t, list)
}
