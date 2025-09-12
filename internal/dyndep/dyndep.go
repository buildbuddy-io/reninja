package dyndep

import (
	"fmt"

	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/dyndep_parser"
	"github.com/buildbuddy-io/gin/internal/explanations"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/state"
)

type Dyndeps = dyndep_parser.Dyndeps

// / Store data loaded from one dyndep file.  Map from an edge
// / to its dynamically-discovered dependency information.
// / This is a struct rather than a typedef so that we can
// / forward-declare it in other headers.
type DyndepFile = map[*graph.Edge]*Dyndeps

// / DyndepLoader loads dynamically discovered dependencies, as
// / referenced via the "dyndep" attribute in build files.
type DyndepLoader struct {
	state         *state.State
	diskInterface disk.Interface
	explanations  *explanations.OptionalExplanations
}

func NewDyndepLoader(state *state.State, diskInterface disk.Interface) *DyndepLoader {
	return &DyndepLoader{
		state:         state,
		diskInterface: diskInterface,
		explanations:  explanations.NewOptional(nil),
	}
}

func (l *DyndepLoader) LoadDyndeps(node *graph.Node, ddf DyndepFile) error {
	node.SetDyndepPending(false)
	l.explanations.Record(node, "loading dyndep file '%s'", node.Path())

	ddf, err := l.LoadDyndepFile(node)
	if err != nil {
		return err
	}

	// Update each edge that specified this node as its dyndep binding.
	for _, edge := range node.OutEdges() {
		if edge.Dyndep() != node {
			continue
		}
		ddi, ok := ddf[edge]
		if !ok {
			return fmt.Errorf("'%s' not mentioned in its dyndep file '%s'", edge.Outputs()[0].Path(), node.Path())
		}
		ddi.Used = true
		if err := l.UpdateEdge(edge, ddi); err != nil {
			return err
		}
	}

	// Reject extra outputs in dyndep file.
	for edge, dyndepOutput := range ddf {
		if !dyndepOutput.Used {
			return fmt.Errorf("dyndep file '%s' mentions output '%s' whose build statement does not have a dyndep binding for the file", node.Path(), edge.Outputs()[0].Path())
		}
	}

	return nil
}

func (l *DyndepLoader) UpdateEdge(edge *graph.Edge, dyndeps *Dyndeps) error {
	// Add dyndep-discovered bindings to the edge.
	// We know the edge already has its own binding
	// scope because it has a "dyndep" binding.
	if dyndeps.Restat {
		edge.Env().AddBinding("restat", "1")
	}

	for _, implicitOut := range dyndeps.ImplicitOutputs {
		edge.AddOutput(implicitOut)
	}
	edge.SetImplicitOuts(len(edge.ImplicitOutputs()) + len(dyndeps.ImplicitOutputs))

	// Add this edge as incoming to each new output.
	for _, node := range dyndeps.ImplicitOutputs {
		if node.InEdge() != nil {
			// This node already has an edge producing it.
			return fmt.Errorf("multiple rules generate %s", node.Path())
		}
		node.SetInEdge(edge)
	}

	// Add the dyndep-discovered inputs to the edge.
	for _, implicitIn := range dyndeps.ImplicitInputs {
		edge.AddInput(implicitIn)
	}
	edge.SetImplicitDeps(len(edge.ImplicitInputs()) + len(dyndeps.ImplicitInputs))

	// Add this edge as outgoing from each new input.
	for _, node := range dyndeps.ImplicitInputs {
		node.AddOutEdge(edge)
	}
	return nil
}

func (l *DyndepLoader) LoadDyndepFile(file *graph.Node) (DyndepFile, error) {
	dyndepFile := make(map[*graph.Edge]*Dyndeps, 0)
	parser := dyndep_parser.New(l.state, l.diskInterface, dyndepFile)
	return dyndepFile, parser.Load(file.Path())
}
