package remote_flags

import (
	"flag"
	"net/url"
	"strings"
	"sync"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
	"github.com/buildbuddy-io/reninja/internal/util"
)

var (
	besBackend         = flag.String("bes_backend", "", "BES backend target, like remote.buildbuddy.io")
	remoteCache        = flag.String("remote_cache", "", "Remote cache target, like remote.buildbuddy.io")
	remoteExecutor     = flag.String("remote_executor", "", "Remote execution target, like remote.buildbuddy.io")
	resultsURL         = flag.String("results_url", "", "BuildBuddy results URL")
	invocationID       = flag.String("invocation_id", "", "Invocation ID to use (auto-generated if not specified)")
	remoteInstanceName = flag.String("remote_instance_name", "", "Cache namespace. Generally should be left unset.")
	projectRoot        = flag.String("project_root", "", "Project root directory for remote execution. Auto-detected from .gclient/.git if not set.")
	digestFunction     = flag.String("digest_function", "BLAKE3", "If set, use this digest function for uploads.")
	buildMetadata      util.StringList

	// Path munging and stuff. Configure this if your project needs it.
	includeScanning = flag.Bool("enable_include_scanning", true, "If true, scan header files for implicit deps and include in the input root of remotely executed actions")
	containerImage  = flag.String("container_image", "", "Container image for remote execution, e.g. docker://gcr.io/YOUR:IMAGE")
)

func init() {
	flag.Var(&buildMetadata, "build_metadata", "Metadata to include in the build event stream, as KEY=VALUE pairs.")
}

func EnableBES() bool {
	return BESBackend() != ""
}

func EnableCache() bool {
	return RemoteCache() != ""
}

func EnableExec() bool {
	return RemoteExecutor() != ""
}

func BESBackend() string {
	return *besBackend
}

func RemoteCache() string {
	if *remoteCache != "" {
		return *remoteCache
	} else if *remoteExecutor != "" {
		return *remoteExecutor
	}
	return ""
}

func RemoteExecutor() string {
	return *remoteExecutor
}

func ResultsURL() string {
	return *resultsURL
}

func InvocationID() string {
	return *invocationID
}

func RemoteInstanceName() string {
	return *remoteInstanceName
}

func ProjectRoot() string {
	return *projectRoot
}

func BytestreamURIPrefix() string {
	cacheTarget := RemoteCache()
	if !strings.Contains(cacheTarget, "://") {
		cacheTarget = "grpcs://" + cacheTarget
	}
	backendURL, err := url.Parse(cacheTarget)
	if err != nil {
		return ""
	}
	return "bytestream://" + backendURL.Host
}

func parseDigestFuncString() repb.DigestFunction_Value {
	if df, ok := repb.DigestFunction_Value_value[strings.ToUpper(*digestFunction)]; ok {
		return repb.DigestFunction_Value(df)
	}
	return repb.DigestFunction_UNKNOWN
}

func DigestFunction() repb.DigestFunction_Value {
	once := sync.OnceValue(parseDigestFuncString)
	return once()
}

func IncludeScanning() bool {
	return *includeScanning
}

func ContainerImage() string {
	return *containerImage
}

// BuildMetadata returns key-value pairs from --build_metadata flags.
// Values override auto-detected metadata for matching keys.
func BuildMetadata() map[string]string {
	m := make(map[string]string, len(buildMetadata))
	for _, kv := range buildMetadata {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			util.Warningf("Ignoring malformed --build_metadata value %q (format is 'KEY=VALUE')", kv)
			continue
		}
		m[k] = v
	}
	return m
}
