package graphviz

import (
	"fmt"
	"strings"
	
	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/dyndep"
	"github.com/buildbuddy-io/gin/internal/dyndep_parser"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/util"
)
	
type Graphviz struct {
	dyndepLoader         *dyndep.DyndepLoader
	visitedNodes map[*graph.Node]struct{}
	visitedEdges map[*graph.Edge]struct{}
}

func New(state *state.State, diskInterface disk.Interface) *Graphviz {
	return &Graphviz{
		dyndepLoader: dyndep.NewDyndepLoader(state, diskInterface),
		visitedNodes: make(map[*graph.Node]struct{}, 0),
		visitedEdges: make(map[*graph.Edge]struct{}, 0),
	}
}

func (v *Graphviz) AddTarget(node *graph.Node) {
	if _, ok := v.visitedNodes[node]; ok {
		return
	}
	pathstr := node.Path()
	pathstr = strings.Replace(pathstr, "\\", "/", -1)
	fmt.Printf("\"%p\" [label=\"%s\"]\n", node, pathstr)
	v.visitedNodes[node] = struct{}{}

	edge := node.InEdge()
	if edge == nil {
		// Leaf node.
		// Draw as rect?
		return
	}

	if _, ok := v.visitedEdges[edge]; ok {
		return
	}
	v.visitedEdges[edge] = struct{}{}

	if edge.Dyndep() != nil && edge.Dyndep().DyndepPending() {
		ddf := dyndep_parser.NewDyndepFile()
		if err := v.dyndepLoader.LoadDyndeps(edge.Dyndep(), ddf); err != nil {
			util.Warningf("%s\n", err)
		}
	}

	if len(edge.Inputs()) == 1 && len(edge.Outputs()) == 1 {
		// Can draw simply.
		// Note extra space before label text -- this is cosmetic and feels
		// like a graphviz bug.
		fmt.Printf("\"%p\" -> \"%p\" [label=\" %s\"]\n", edge.Inputs()[0], edge.Outputs()[0], edge.Rule().Name())
	} else {
		fmt.Printf("\"%p\" [label=\"%s\", shape=ellipse]\n", edge, edge.Rule().Name())
		for _, out := range edge.Outputs() {
			fmt.Printf("\"%p\" -> \"%p\"\n", edge, out)
		}
		for i, in := range edge.Inputs() {
			orderOnly := ""
			if edge.IsOrderOnly(i) {
				orderOnly = " style=dotted"
			}
			fmt.Printf("\"%p\" -> \"%p\" [arrowhead=none%s]\n", in, edge, orderOnly)
		}
	}

	for _, in := range edge.Inputs() {
		v.AddTarget(in)
	}
}

func (v *Graphviz) Start() {
	fmt.Printf("digraph ninja {\n")
	fmt.Printf("rankdir=\"LR\"\n")
	fmt.Printf("node [fontsize=10, shape=box, height=0.25]\n")
	fmt.Printf("edge [fontsize=10]\n")
}

func (v *Graphviz) Finish() {
	fmt.Printf("}\n")
}

