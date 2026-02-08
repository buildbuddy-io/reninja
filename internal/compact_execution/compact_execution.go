package compact_execution

import (
	"io"
	"sync"

	"github.com/buildbuddy-io/reninja/internal/digest"
	"github.com/buildbuddy-io/reninja/internal/graph"
	"github.com/buildbuddy-io/reninja/internal/remote_flags"
	"github.com/buildbuddy-io/reninja/internal/spawn"
	"github.com/google/shlex"
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	spawnpb "github.com/buildbuddy-io/reninja/genproto/spawn"
)

type Log struct {
	mu *sync.Mutex

	logFile io.Writer

	// nextID is the next ID to assign to an entry.
	nextID uint32

	// fileIDs maps file paths to their assigned entry IDs.
	fileIDs map[string]uint32
}

func New(logFile io.Writer) *Log {
	return &Log{
		mu:      &sync.Mutex{},
		logFile: logFile,
		nextID:  1,
		fileIDs: make(map[string]uint32),
	}
}

func (l *Log) writeEntry(entry *spawnpb.ExecLogEntry) error {
	_, err := protodelim.MarshalTo(l.logFile, entry)
	return err
}

func (l *Log) allocID() uint32 {
	id := l.nextID
	l.nextID++
	return id
}

func (l *Log) createInputSet(inputIDs []uint32) (uint32, error) {
	if len(inputIDs) == 0 {
		return 0, nil
	}

	l.mu.Lock()
	id := l.allocID()
	l.mu.Unlock()

	entry := &spawnpb.ExecLogEntry{
		Id: id,
		Type: &spawnpb.ExecLogEntry_InputSet_{
			InputSet: &spawnpb.ExecLogEntry_InputSet{
				InputIds: inputIDs,
			},
		},
	}

	if err := l.writeEntry(entry); err != nil {
		return 0, err
	}

	return id, nil
}

// getOrCreateFileEntry returns the ID for a file, creating the entry if needed.
// The path should be relative to the execution root.
func (l *Log) getOrCreateFileEntry(path string) (uint32, error) {
	l.mu.Lock()

	if id, ok := l.fileIDs[path]; ok {
		l.mu.Unlock()
		return id, nil
	}

	l.mu.Unlock() // Unlock while computing file digest.
	d, err := digest.ComputeForFile(path, remote_flags.DigestFunction())
	if err != nil {
		return 0, err
	}

	l.mu.Lock() // Check again that this path has not been mapped.
	if id, ok := l.fileIDs[path]; ok {
		l.mu.Unlock()
		return id, nil
	}
	id := l.allocID()
	l.fileIDs[path] = id
	l.mu.Unlock()

	digestProto := &spawnpb.Digest{
		Hash:      d.GetHash(),
		SizeBytes: d.GetSizeBytes(),
	}

	entry := &spawnpb.ExecLogEntry{
		Id: id,
		Type: &spawnpb.ExecLogEntry_File_{
			File: &spawnpb.ExecLogEntry_File{
				Path:   path,
				Digest: digestProto,
			},
		},
	}

	if err := l.writeEntry(entry); err != nil {
		return 0, err
	}

	return id, nil
}

// RecordEdge records an executed edge to the spawn log.
// This should be called after the edge has finished executing.
func (l *Log) RecordEdge(edge *graph.Edge, result *spawn.Result) error {
	if edge.IsPhony() {
		return nil // Skip phony edges.
	}

	command := edge.EvaluateCommand(true)
	args, err := shlex.Split(command)
	if err != nil {
		return err
	}

	var inputIDs []uint32
	for _, input := range edge.Inputs() {
		id, err := l.getOrCreateFileEntry(input.Path())
		if err != nil {
			return err
		}
		inputIDs = append(inputIDs, id)
	}

	inputSetID, err := l.createInputSet(inputIDs)
	if err != nil {
		return err
	}

	var outputs []*spawnpb.ExecLogEntry_Output
	for _, output := range edge.Outputs() {
		id, err := l.getOrCreateFileEntry(output.Path())
		if err != nil {
			return err
		}
		outputs = append(outputs, &spawnpb.ExecLogEntry_Output{
			Type: &spawnpb.ExecLogEntry_Output_OutputId{
				OutputId: id,
			},
		})
	}

	// Build metrics.
	var metrics *spawnpb.SpawnMetrics
	if result != nil && !result.Start.IsZero() && !result.End.IsZero() {
		wallTime := result.End.Sub(result.Start)
		metrics = &spawnpb.SpawnMetrics{
			TotalTime:         durationpb.New(wallTime),
			ExecutionWallTime: durationpb.New(wallTime),
			StartTime:         timestamppb.New(result.Start),
			InputFiles:        int64(len(edge.Inputs())),
		}
	}

	// Build the spawn entry.
	spawnEntry := &spawnpb.ExecLogEntry_Spawn{
		Args:        args,
		InputSetId:  inputSetID,
		Outputs:     outputs,
		TargetLabel: edge.TargetLabel(),
		Mnemonic:    edge.ActionMnemonic(),
		Cacheable:   true,
		Remotable:   true,
		Metrics:     metrics,
	}

	if result != nil {
		spawnEntry.ExitCode = int32(result.Status)
		spawnEntry.Status = result.Output
		spawnEntry.Runner = result.Runner
		spawnEntry.CacheHit = result.CacheHit
	}

	entry := &spawnpb.ExecLogEntry{
		// Spawn entries don't need an ID as they're not referenced.
		Type: &spawnpb.ExecLogEntry_Spawn_{
			Spawn: spawnEntry,
		},
	}

	return l.writeEntry(entry)
}
