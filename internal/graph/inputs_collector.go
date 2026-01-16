package graph

import (
	"runtime"

	"github.com/buildbuddy-io/reninja/internal/util"
)

type InputsCollector struct {
	inputs  []*Node
	visited map[*Node]struct{}
}

func NewInputsCollector() *InputsCollector {
	return &InputsCollector{
		inputs:  make([]*Node, 0),
		visited: make(map[*Node]struct{}),
	}
}

func (c *InputsCollector) VisitNode(n *Node) {
	e := n.InEdge()
	if e == nil {
		return
	}

	// Add inputs of the producing edge to the result,
	// except if they are themselves produced by a phony
	// edge.
	for _, input := range e.Inputs() {
		_, alreadySeen := c.visited[input]
		if alreadySeen {
			continue
		}
		c.visited[input] = struct{}{}
		c.VisitNode(input)
		inputEdge := input.InEdge()
		if !(inputEdge != nil && inputEdge.IsPhony()) {
			c.inputs = append(c.inputs, input)
		}
	}
}

func (c *InputsCollector) GetInputsAsStrings(shellEscape bool) []string {
	result := make([]string, len(c.inputs))
	for i, input := range c.inputs {
		unescaped := input.PathDecanonicalized()
		var path string
		if shellEscape {
			if runtime.GOOS == "windows" {
				path = util.GetWin32EscapedString(unescaped)
			} else {
				path = util.GetShellEscapedString(unescaped)
			}
			result[i] = path
		} else {
			result[i] = unescaped
		}
	}
	return result
}

func (c *InputsCollector) Reset() {
	c.inputs = c.inputs[:0]
	for k := range c.visited {
		delete(c.visited, k)
	}
}
