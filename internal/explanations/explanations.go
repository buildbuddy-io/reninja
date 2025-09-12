package explanations

import (
	"fmt"
)

type Explanations struct {
	items map[any][]string
}

func New() *Explanations {
	return &Explanations{
		items: make(map[any][]string),
	}
}

func (e *Explanations) Record(key any, formatString string, args ...interface{}) {
	e.items[key] = append(e.items[key], fmt.Sprintf(formatString, args...))
}

func (e *Explanations) LookupAndAppend(key any) []string {
	return e.items[key]
}

type OptionalExplanations struct {
	explanations *Explanations
}

func NewOptional(explanations *Explanations) *OptionalExplanations {
	return &OptionalExplanations{
		explanations: explanations,
	}
}

func (o *OptionalExplanations) Record(key any, formatString string, args ...interface{}) {
	if o.explanations != nil {
		o.explanations.Record(key, formatString, args...)
	}
}

func (o *OptionalExplanations) LookupAndAppend(key any) []string {
	if o.explanations != nil {
		return o.explanations.LookupAndAppend(key)
	}
	return nil
}
