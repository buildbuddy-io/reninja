package bes_event

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/buildbuddy-io/gin/internal/digest"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/version"

	bespb "github.com/buildbuddy-io/gin/genproto/build_event_stream"
	bepb "github.com/buildbuddy-io/gin/genproto/build_events"
	clpb "github.com/buildbuddy-io/gin/genproto/command_line"
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

func StructuredCommandLineEvent(cmdArgs []string) *bespb.BuildEvent {
	executableName := os.Args[0]
	sections := []*clpb.CommandLineSection{
		{
			SectionLabel: "command",
			SectionType: &clpb.CommandLineSection_ChunkList{
				ChunkList: &clpb.ChunkList{
					Chunk: []string{executableName},
				},
			},
		},
		{
			SectionLabel: "executable",
			SectionType: &clpb.CommandLineSection_ChunkList{
				ChunkList: &clpb.ChunkList{
					Chunk: []string{executableName},
				},
			},
		},
	}

	if len(cmdArgs) > 0 {
		sections = append(sections, &clpb.CommandLineSection{
			SectionLabel: "arguments",
			SectionType: &clpb.CommandLineSection_ChunkList{
				ChunkList: &clpb.ChunkList{
					Chunk: cmdArgs,
				},
			},
		})
	}

	commandLine := &clpb.CommandLine{
		CommandLineLabel: "original",
		Sections:         sections,
	}

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

func TargetCompletedEvent(targetLabel string, exitCode exit_status.ExitStatusType) *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{Id: &bespb.BuildEventId_TargetCompleted{
			TargetCompleted: &bespb.BuildEventId_TargetCompletedId{
				Label: targetLabel,
			},
		}},
		Payload: &bespb.BuildEvent_Completed{Completed: &bespb.TargetComplete{
			Success: exitCode == exit_status.ExitSuccess,
			//                                OutputGroup: []*bespb.OutputGroup{
			//                                        {
			//                                                FileSets: []*bespb.BuildEventId_NamedSetOfFilesId{
			//                                                        {Id: namedSetID},
			//                                                },
			//                                        },
			//                                },
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

func BuildToolLogsEvent(bytestreamURIPrefix string, commandProfileGz *digest.CASResourceName) *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_BuildToolLogs{},
		},
		Payload: &bespb.BuildEvent_BuildToolLogs{
			BuildToolLogs: &bespb.BuildToolLogs{
				Log: []*bespb.File{
					{
						Name: "command.profile.gz",
						File: &bespb.File_Uri{
							Uri: fmt.Sprintf("%s%s", bytestreamURIPrefix, commandProfileGz.DownloadString()),
						},
					},
				},
			},
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
