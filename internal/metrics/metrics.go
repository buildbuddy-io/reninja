package metrics

import (
	"fmt"
	"time"
)

var DefaultMetrics *Metrics

func Enable() {
	DefaultMetrics = &Metrics{
		metricSet: make(map[string]*Metric, 0),
		metrics:   make([]*Metric, 0),
	}
}

func Disable() {
	DefaultMetrics = nil
}

// A single metrics we're tracking, like "depfile load time".
type Metric struct {
	name string
	// Number of times we've hit the code path.
	count int
	// Total time (in platform-dependent units) we've spent on the code path.
	sum time.Duration
}

// The singleton that stores metrics and prints the report.
type Metrics struct {
	metricSet map[string]*Metric
	metrics   []*Metric
}

func (m *Metrics) NewMetric(name string) *Metric {
	if met, ok := m.metricSet[name]; ok {
		return met
	}
	met := &Metric{
		name: name,
	}
	m.metricSet[name] = met
	m.metrics = append(m.metrics, met)
	return met
}

// Report prints a summary report to stdout.
func (m *Metrics) Report() {
	width := 0
	for _, metric := range m.metrics {
		width = max(len(metric.name), width)
	}

	fmt.Printf("%-*s\t%-6s\t%-9s\t%s\n", width,
		"metric", "count", "avg (us)", "total (ms)")
	for _, metric := range m.metrics {
		micros := metric.sum.Microseconds()
		total := float64(micros) / 1000.0
		avg := float64(micros) / float64(metric.count)
		fmt.Printf("%-*s\t%-6d\t%-8.1f\t%.1f\n", width, metric.name,
			metric.count, avg, total)
	}
}

type DoneFunction func()

// The primary interface to metrics.  Use defer metric.Record("foobar") at the top
// of a function to get timing stats recorded for each call of the function.
func Record(name string) DoneFunction {
	return RecordIf(name, func() bool { return true })
}

// A variant of Record that doesn't record anything if condition returns false.
func RecordIf(name string, condition func() bool) DoneFunction {
	if DefaultMetrics == nil {
		return func() {}
	}

	start := time.Now()
	return func() {
		if !condition() {
			return
		}
		m := DefaultMetrics.NewMetric(name)
		m.count += 1
		m.sum += time.Now().Sub(start)
	}
}
