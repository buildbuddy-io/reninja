package span

// Probably could use some otel thingy here but perf...

import (
	"context"
	"fmt"
	"time"
)

type spanTraceKey string

const defaultTraceKey = spanTraceKey("trace-events")

type Event struct {
	Name  string
	Start time.Time
	End   time.Time
}

type eventPackage struct {
	Start  time.Time
	Events []Event
}

// BeginTracing returns a new context that is span-tracing enabled.
func BeginTracing(ctx context.Context) context.Context {
	pkg := &eventPackage{
		Start:  time.Now(),
		Events: nil,
	}
	return context.WithValue(ctx, defaultTraceKey, pkg)
}

// Events returns all events recorded on context.
func Events(ctx context.Context) []Event {
	if v := ctx.Value(defaultTraceKey); v != nil {
		if pkg, ok := v.(*eventPackage); ok {
			return pkg.Events
		}
	}
	return nil
}

type DoneFunction func()

// Record will add a new event to ctx with eventName when DoneFunction
// is called.
func Record(ctx context.Context, eventName string) DoneFunction {
	return RecordIf(ctx, eventName, func() bool { return true })
}

// A variant of Record that doesn't record anything if condition returns false.
func RecordIf(ctx context.Context, eventName string, condition func() bool) DoneFunction {
	v := ctx.Value(defaultTraceKey)
	if v == nil {
		return func() {}
	}
	pkg, ok := v.(*eventPackage)
	if !ok {
		return func() {}
	}

	funcStart := time.Now()
	return func() {
		if !condition() {
			return
		}
		pkg.Events = append(pkg.Events, Event{
			Name:  eventName,
			Start: funcStart,
			End:   time.Now(),
		})
		fmt.Printf("recorded %q, events: %+v", eventName, pkg.Events)
	}
}
