package clean

import (
	"fmt"
	"maps"
	"slices"

	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/build_log"
	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/dyndep"
	"github.com/buildbuddy-io/gin/internal/dyndep_parser"
	"github.com/buildbuddy-io/gin/internal/eval_env"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/util"
)

// Cleaner removes built files
type Cleaner struct {
	state             *state.State
	config            *build_config.Config
	dyndepLoader      *dyndep.DyndepLoader
	removed           map[string]struct{}
	cleaned           map[*graph.Node]struct{}
	cleanedFilesCount int
	diskInterface     disk.Interface
	status            int
}

// New creates a new Cleaner
func NewCleaner(state *state.State, config *build_config.Config, diskInterface disk.Interface) *Cleaner {
	return &Cleaner{
		state:         state,
		config:        config,
		dyndepLoader:  dyndep.NewDyndepLoader(state, diskInterface),
		removed:       make(map[string]struct{}),
		cleaned:       make(map[*graph.Node]struct{}),
		diskInterface: diskInterface,
	}
}

func (c *Cleaner) IsVerbose() bool {
	return c.config.Verbosity != build_config.Quiet &&
		(c.config.Verbosity == build_config.Verbose || c.config.DryRun)
}

func (c *Cleaner) RemoveFile(path string) int {
	return c.diskInterface.RemoveFile(path)
}

func (c *Cleaner) FileExists(path string) bool {
	mtime, err := c.diskInterface.Stat(path)
	if err != nil {
		util.Errorf("%s", err)
	}
	return mtime > 0 // Treat Stat() errors as "file does not exist"
}

func (c *Cleaner) Report(path string) {
	c.cleanedFilesCount++
	if c.IsVerbose() {
		fmt.Printf("Remove %s\n", path)
	}
}

func (c *Cleaner) IsAlreadyRemoved(path string) bool {
	_, ok := c.removed[path]
	return ok
}

func (c *Cleaner) Remove(path string) {
	if !c.IsAlreadyRemoved(path) {
		c.removed[path] = struct{}{}
		if c.config.DryRun {
			if c.FileExists(path) {
				c.Report(path)
			}
		} else {
			ret := c.RemoveFile(path)
			if ret == 0 {
				c.Report(path)
			} else if ret == -1 {
				c.status = 1
			}
		}
	}
}

func (c *Cleaner) RemoveEdgeFiles(edge *graph.Edge) {
	depfile := edge.GetUnescapedDepfile()
	if depfile != "" {
		c.Remove(depfile)
	}

	rspfile := edge.GetUnescapedRspfile()
	if rspfile != "" {
		c.Remove(rspfile)
	}
}

func (c *Cleaner) PrintHeader() {
	if c.config.Verbosity == build_config.Quiet {
		return
	}
	fmt.Printf("Cleaning...")
	if c.IsVerbose() {
		fmt.Printf("\n")
	} else {
		fmt.Printf(" ")
	}
}

func (c *Cleaner) PrintFooter() {
	if c.config.Verbosity == build_config.Quiet {
		return
	}
	fmt.Printf("%d files.\n", c.cleanedFilesCount)
}

func (c *Cleaner) CleanAll(generator bool) int {
	c.Reset()
	c.PrintHeader()
	c.LoadDyndeps()
	for _, e := range c.state.Edges() {
		// Do not try to remove phony targets
		if e.IsPhony() {
			continue
		}
		// Do not remove generator's files unless generator specified.
		if !generator && e.GetBindingBool("generator") {
			continue
		}
		for _, outNode := range e.Outputs() {
			c.Remove(outNode.Path())
		}

		c.RemoveEdgeFiles(e)
	}
	c.PrintFooter()
	return c.status
}

func (c *Cleaner) CleanDead(entries build_log.Entries) int {
	c.Reset()
	c.PrintHeader()
	c.LoadDyndeps()
	for k := range entries {
		n := c.state.LookupNode(k)
		// Detecting stale outputs works as follows:
		//
		// - If it has no Node, it is not in the build graph, or the deps log
		//   anymore, hence is stale.
		//
		// - If it isn't an output or input for any edge, it comes from a stale
		//   entry in the deps log, but no longer referenced from the build
		//   graph.
		//
		if n == nil || (n.InEdge() == nil && len(n.OutEdges()) == 0) {
			c.Remove(k)
		}
	}
	c.PrintFooter()
	return c.status
}

func (c *Cleaner) DoCleanTarget(target *graph.Node) {
	e := target.InEdge()
	if e != nil {
		// Do not try to remove phony targets
		if !e.IsPhony() {
			c.Remove(target.Path())
			c.RemoveEdgeFiles(e)
		}
		for _, n := range e.Inputs() {
			// call DoCleanTarget recursively if this node has not been visited
			if _, ok := c.cleaned[n]; !ok {
				c.DoCleanTarget(n)
			}
		}
	}
	// mark this target to be cleaned already
	c.cleaned[target] = struct{}{}
}

func (c *Cleaner) CleanTarget(target *graph.Node) int {
	if target == nil {
		panic("target should be non-nil")
	}

	c.Reset()
	c.PrintHeader()
	c.LoadDyndeps()
	c.DoCleanTarget(target)
	c.PrintFooter()
	return c.status
}

func (c *Cleaner) CleanTargetByName(targetName string) int {
	if targetName == "" {
		panic("targetName should not be empty")
	}
	c.Reset()
	node := c.state.LookupNode(targetName)
	if node != nil {
		c.CleanTarget(node)
	} else {
		util.Errorf("unknown target '%s'", targetName)
		c.status = 1
	}
	return c.status
}

func (c *Cleaner) CleanTargets(targets []string) int {
	c.Reset()
	c.PrintHeader()
	c.LoadDyndeps()
	for _, targetName := range targets {
		if targetName == "" {
			util.Errorf("failed to cananicalize '': empty path")
			c.status = 1
			continue
		}
		targetName, _ = util.CanonicalizePath(targetName)
		target := c.state.LookupNode(targetName)
		if target != nil {
			if c.IsVerbose() {
				fmt.Printf("Target %s\n", targetName)
			}
			c.DoCleanTarget(target)
		} else {
			util.Errorf("unknown target '%s'", targetName)
			c.status = 1
		}
	}
	c.PrintFooter()
	return c.status
}

func (c *Cleaner) DoCleanRule(rule *eval_env.Rule) {
	if rule == nil {
		panic("rule should be non-nil")
	}
	for _, e := range c.state.Edges() {
		if e.Rule().Name() == rule.Name() {
			for _, outNode := range e.Outputs() {
				c.Remove(outNode.Path())
				c.RemoveEdgeFiles(e)
			}
		}
	}
}

func (c *Cleaner) CleanRule(rule *eval_env.Rule) int {
	if rule == nil {
		panic("rule should be non-nil")
	}

	c.Reset()
	c.PrintHeader()
	c.LoadDyndeps()
	c.DoCleanRule(rule)
	c.PrintFooter()
	return c.status
}

func (c *Cleaner) CleanRuleByName(ruleName string) int {
	if ruleName == "" {
		panic("ruleName should not be empty")
	}
	c.Reset()
	r, ok := c.state.Bindings().LookupRule(ruleName)
	if ok && r != nil {
		c.CleanRule(r)
	} else {
		util.Errorf("unknown rule '%s'", ruleName)
		c.status = 1
	}
	return c.status
}

func (c *Cleaner) CleanRules(ruleNames []string) int {
	if len(ruleNames) == 0 {
		panic("ruleNames should not be empty")
	}

	c.Reset()
	c.PrintHeader()
	c.LoadDyndeps()
	for _, ruleName := range ruleNames {
		rule, ok := c.state.Bindings().LookupRule(ruleName)
		if ok && rule != nil {
			if c.IsVerbose() {
				fmt.Printf("Rule %s\n", ruleName)
			}
			c.DoCleanRule(rule)
		} else {
			util.Errorf("unknown rule '%s'", ruleName)
			c.status = 1
		}
	}
	c.PrintFooter()
	return c.status
}

func (c *Cleaner) Reset() {
	c.status = 0
	c.cleanedFilesCount = 0
	for key := range c.removed {
		delete(c.removed, key)
	}
	for key := range c.cleaned {
		delete(c.cleaned, key)
	}
}

func (c *Cleaner) LoadDyndeps() {
	// Load dyndep files that exist, before they are cleaned.
	for _, e := range c.state.Edges() {
		dyndep := e.Dyndep()
		if dyndep != nil && dyndep.DyndepPending() {
			// Capture and ignore errors loading the dyndep file.
			// We clean as much of the graph as we know.
			ddf := dyndep_parser.NewDyndepFile()
			_ = c.dyndepLoader.LoadDyndeps(dyndep, ddf)
		}
	}
}

func (c *Cleaner) TestingCleanedFilesCount() int {
	return c.cleanedFilesCount
}

func (c *Cleaner) TestingFilesRemoved() []string {
	return slices.Sorted(maps.Keys(c.removed))
}
