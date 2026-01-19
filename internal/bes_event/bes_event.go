package bes_event

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/buildbuddy-io/reninja/internal/digest"
	"github.com/buildbuddy-io/reninja/internal/exit_status"
	"github.com/buildbuddy-io/reninja/internal/version"

	bespb "github.com/buildbuddy-io/reninja/genproto/build_event_stream"
	bepb "github.com/buildbuddy-io/reninja/genproto/build_events"
	clpb "github.com/buildbuddy-io/reninja/genproto/command_line"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

func StartedEvent(toolName string, cmdArgs []string, invocationID string, startTime time.Time) *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_Started{},
		},
		Children: []*bespb.BuildEventId{
			{Id: &bespb.BuildEventId_BuildMetadata{}},
			{Id: &bespb.BuildEventId_WorkspaceStatus{}},
			{Id: &bespb.BuildEventId_Configuration{Configuration: &bespb.BuildEventId_ConfigurationId{Id: "host"}}},
			{Id: &bespb.BuildEventId_BuildFinished{}},
			{Id: &bespb.BuildEventId_StructuredCommandLine{
				StructuredCommandLine: &bespb.BuildEventId_StructuredCommandLineId{
					CommandLineLabel: "original",
				},
			}},
		},
		Payload: &bespb.BuildEvent_Started{
			Started: &bespb.BuildStarted{
				Uuid:               invocationID,
				BuildToolVersion:   version.NinjaVersion,
				StartTime:          timestamppb.New(startTime),
				Command:            toolName,
				OptionsDescription: strings.Join(cmdArgs, " "),
			},
		},
	}
}

func OptionsParsedEvent(cmdArgs []string) *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Payload: &bespb.BuildEvent_OptionsParsed{
			OptionsParsed: &bespb.OptionsParsed{
				CmdLine:         os.Args[1:],
				ExplicitCmdLine: os.Args[1:],
			},
		},
	}
}

func getStructuredCommandLine() *clpb.CommandLine {
	options := make([]*clpb.Option, 0, len(os.Args[1:]))
	for _, arg := range os.Args[1:] {
		// TODO: Handle other arg formats ("-name=value", "--name value",
		// "--bool_switch", etc). Ignore these for now since we don't set
		// them in practice.
		if !strings.HasPrefix(arg, "--") || !strings.Contains(arg, "=") {
			continue
		}
		nameValue := strings.SplitN(strings.TrimPrefix(arg, "--"), "=", 2)
		options = append(options, &clpb.Option{
			CombinedForm: arg,
			OptionName:   nameValue[0],
			OptionValue:  nameValue[1],
		})
	}
	return &clpb.CommandLine{
		CommandLineLabel: "original",
		Sections: []*clpb.CommandLineSection{
			{
				SectionLabel: "executable",
				SectionType: &clpb.CommandLineSection_ChunkList{ChunkList: &clpb.ChunkList{
					Chunk: []string{os.Args[0]},
				}},
			},
			{
				SectionLabel: "command options",
				SectionType: &clpb.CommandLineSection_OptionList{OptionList: &clpb.OptionList{
					Option: options,
				}},
			},
		},
	}
}

func StructuredCommandLineEvent(cmdArgs []string) *bespb.BuildEvent {
	commandLine := getStructuredCommandLine()

	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_StructuredCommandLine{
				StructuredCommandLine: &bespb.BuildEventId_StructuredCommandLineId{
					CommandLineLabel: "original",
				},
			},
		},
		Payload: &bespb.BuildEvent_StructuredCommandLine{
			StructuredCommandLine: commandLine,
		},
	}
}

func BuildMetadataEvent(kvs map[string]string) *bespb.BuildEvent {
	kvs["ROLE"] = "NINJA"
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_BuildMetadata{},
		},
		Payload: &bespb.BuildEvent_BuildMetadata{
			BuildMetadata: &bespb.BuildMetadata{
				Metadata: kvs,
			},
		},
	}
}

func WorkspaceStatusEvent() *bespb.BuildEvent {
	user := os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "unknown"
	}

	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_WorkspaceStatus{},
		},
		Payload: &bespb.BuildEvent_WorkspaceStatus{
			WorkspaceStatus: &bespb.WorkspaceStatus{
				Item: []*bespb.WorkspaceStatus_Item{
					{Key: "BUILD_USER", Value: user},
					{Key: "BUILD_HOST", Value: hostname},
					{Key: "BUILD_WORKING_DIRECTORY", Value: cwd},
				},
			},
		},
	}

}

func ConfigurationEvent() *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_Configuration{
				Configuration: &bespb.BuildEventId_ConfigurationId{
					Id: "host",
				},
			},
		},
		Payload: &bespb.BuildEvent_Configuration{
			Configuration: &bespb.Configuration{
				Mnemonic:     "host",
				PlatformName: runtime.GOOS,
				Cpu:          runtime.GOARCH,
				MakeVariable: map[string]string{
					"TARGET_CPU": runtime.GOARCH,
				},
			},
		},
	}

}

func TargetConfiguredEvent(targetLabel, targetKind, configID string) *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_TargetConfigured{
				TargetConfigured: &bespb.BuildEventId_TargetConfiguredId{
					Label: targetLabel,
				},
			}},
		Payload: &bespb.BuildEvent_Configured{Configured: &bespb.TargetConfigured{
			TargetKind: targetKind,
		}},
	}
}

func NamedSetOfFilesEvent(namedSetID string, files []*bespb.File) *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{Id: &bespb.BuildEventId_NamedSet{
			NamedSet: &bespb.BuildEventId_NamedSetOfFilesId{Id: namedSetID},
		}},
		Payload: &bespb.BuildEvent_NamedSetOfFiles{NamedSetOfFiles: &bespb.NamedSetOfFiles{
			Files: files,
		}},
	}
}

func TargetCompletedEvent(targetLabel string, exitCode exit_status.ExitStatusType) *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{Id: &bespb.BuildEventId_TargetCompleted{
			TargetCompleted: &bespb.BuildEventId_TargetCompletedId{
				Label: targetLabel,
			},
		}},
		Payload: &bespb.BuildEvent_Completed{Completed: &bespb.TargetComplete{
			Success: exitCode == exit_status.ExitSuccess,
			OutputGroup: []*bespb.OutputGroup{
				{
					FileSets: []*bespb.BuildEventId_NamedSetOfFilesId{
						{Id: targetLabel},
					},
				},
			},
		}},
	}
}

func BuildMetricsEvent(actionsCreated, actionsExecuted, cpuTimeMillis, wallTimeMillis int64) *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_BuildMetrics{},
		},
		Payload: &bespb.BuildEvent_BuildMetrics{
			BuildMetrics: &bespb.BuildMetrics{
				ActionSummary: &bespb.BuildMetrics_ActionSummary{
					ActionsCreated:                    actionsCreated,
					ActionsCreatedNotIncludingAspects: actionsCreated,
					ActionsExecuted:                   actionsExecuted,
				},
				TimingMetrics: &bespb.BuildMetrics_TimingMetrics{
					CpuTimeInMs:  cpuTimeMillis,
					WallTimeInMs: wallTimeMillis,
				},
			},
		},
	}
}

func BuildToolLogsEvent(bytestreamURIPrefix string, commandProfileGz, execLogBinpbZstd *digest.CASResourceName) *bespb.BuildEvent {
	toolLogs := &bespb.BuildToolLogs{}
	if commandProfileGz != nil {
		toolLogs.Log = append(toolLogs.Log, &bespb.File{
			Name: "command.profile.gz",
			File: &bespb.File_Uri{
				Uri: fmt.Sprintf("%s%s", bytestreamURIPrefix, commandProfileGz.DownloadString()),
			},
		})
	}
	if execLogBinpbZstd != nil {
		toolLogs.Log = append(toolLogs.Log, &bespb.File{
			Name: "execution_log.binpb.zst",
			File: &bespb.File_Uri{
				Uri: fmt.Sprintf("%s%s", bytestreamURIPrefix, execLogBinpbZstd.DownloadString()),
			},
		})
	}
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_BuildToolLogs{},
		},
		Payload: &bespb.BuildEvent_BuildToolLogs{
			BuildToolLogs: toolLogs,
		},
	}

}

func FinishedEvent(exitCode int) *bespb.BuildEvent {
	var exitCodeName string

	// From https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/util/ExitCode.java#L38
	switch exit_status.ExitStatusType(exitCode) {
	case exit_status.ExitSuccess:
		exitCodeName = "SUCCESS"
	case exit_status.ExitInterrupted:
		exitCodeName = "INTERRUPTED"
	case exit_status.ExitFailure:
		exitCodeName = "FAILED"
	}
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_BuildFinished{},
		},
		Children: []*bespb.BuildEventId{
			{Id: &bespb.BuildEventId_BuildToolLogs{}},
		},
		LastMessage: true,
		Payload: &bespb.BuildEvent_Finished{
			Finished: &bespb.BuildFinished{
				ExitCode: &bespb.BuildFinished_ExitCode{
					Name: exitCodeName,
					Code: int32(exitCode),
				},
				FinishTime: timestamppb.Now(),
			},
		},
	}

}

func ConsoleOutputEvent(output string, streamType bepb.ConsoleOutputStream) *bespb.BuildEvent {
	progress := &bespb.Progress{}
	if streamType == bepb.ConsoleOutputStream_STDOUT {
		progress.Stdout = output
	} else {
		progress.Stderr = output
	}

	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_Progress{
				Progress: &bespb.BuildEventId_ProgressId{
					OpaqueCount: int32(time.Now().UnixNano()),
				},
			},
		},
		Payload: &bespb.BuildEvent_Progress{
			Progress: progress,
		},
	}
}
