package remote_flags

import (
	"flag"
	"net/url"
	"strings"
	"sync"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

var (
	enableBES   = flag.Bool("bes", false, "If true, send Build Event Stream (BES) to --bes_backend")
	enableCache = flag.Bool("cache", false, "If true, read and write actions to --remote_cache")
	enableExec  = flag.Bool("exec", false, "If true, execute actions remotely on --remote_executor")

	// Easy defaults
	besBackend         = flag.String("bes_backend", "remote.buildbuddy.io", "BES backend target, like remote.buildbuddy.io")
	remoteCache        = flag.String("remote_cache", "remote.buildbuddy.io", "Remote cache target, like remote.buildbuddy.io")
	remoteExecutor     = flag.String("remote_executor", "remote.buildbuddy.io", "Remote execution target, like remote.buildbuddy.io")
	resultsURL         = flag.String("results_url", "https://app.buildbuddy.io", "BuildBuddy results URL")
	invocationID       = flag.String("invocation_id", "", "Invocation ID to use (auto-generated if not specified)")
	remoteInstanceName = flag.String("remote_instance_name", "", "Cache namespace. Generally should be left unset.")
	projectRoot        = flag.String("project_root", "", "Project root directory for remote execution. Auto-detected from .gclient/.git if not set.")
	digestFunction     = flag.String("digest_function", "BLAKE3", "If set, use this digest function for uploads.")

	// Path munging and stuff. Configure this if your project needs it.
	includeScanning = flag.Bool("enable_include_scanning", false, "If true, scan header files for implicit deps and include in the input root of remotely executed actions")
)

func EnableBES() bool {
	return *enableBES
}

func EnableCache() bool {
	return *enableCache
}

func EnableExec() bool {
	return *enableExec
}

func BESBackend() string {
	return *besBackend
}

func RemoteCache() string {
	return *remoteCache
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
	backendURL, err := url.Parse(RemoteCache())
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
