package remote_flags

import (
	"flag"
)

var (
	besBackend         = flag.String("bes_backend", "", "BES backend target, like remote.buildbuddy.io")
	remoteCache        = flag.String("remote_cache", "", "Remote cache target, like remote.buildbuddy.io")
	resultsURL         = flag.String("results_url", "https://app.buildbuddy.io", "BuildBuddy results URL")
	invocationID       = flag.String("invocation_id", "", "Invocation ID to use (auto-generated if not specified)")
	remoteInstanceName = flag.String("remote_instance_name", "", "Generally should be left unset.")
)

func BESBackend() string {
	return *besBackend
}

func RemoteCache() string {
	return *remoteCache
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
