package command_collector

import (
	"github.com/buildbuddy-io/reninja/internal/graph"
)

type CommandCollector struct {
	visitedNodes map[*graph.Node]struct{}
	visitedEdges map[*graph.Edge]struct{}
	inEdges      []*graph.Edge
}

func New() *CommandCollector {
	return &CommandCollector{
		visitedNodes: make(map[*graph.Node]struct{}, 0),
		visitedEdges: make(map[*graph.Edge]struct{}, 0),
		inEdges:      make([]*graph.Edge, 0),
	}
}
func (c *CommandCollector) InEdges() []*graph.Edge {
	return c.inEdges
}

func (c *CommandCollector) CollectFrom(node *graph.Node) {
	if node == nil {
		panic("node should not be nil")
	}
	if _, ok := c.visitedNodes[node]; ok {
		return
	}
	c.visitedNodes[node] = struct{}{}

	edge := node.InEdge()
	if edge == nil {
		return
	}

	if _, ok := c.visitedEdges[edge]; ok {
		return
	}
	c.visitedEdges[edge] = struct{}{}

	for _, inputNode := range edge.Inputs() {
		c.CollectFrom(inputNode)
	}

	if !edge.IsPhony() {
		c.inEdges = append(c.inEdges, edge)
	}
}
