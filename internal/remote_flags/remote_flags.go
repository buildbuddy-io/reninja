package remote_flags

import (
	"flag"
	"net/url"
)

var (
	enableBES   = flag.Bool("bes", false, "If true, send Build Event Stream (BES) to --bes_backend")
	enableCache = flag.Bool("cache", false, "If true, read and write actions to --remote_cache")

	besBackend         = flag.String("bes_backend", "remote.buildbuddy.io", "BES backend target, like remote.buildbuddy.io")
	remoteCache        = flag.String("remote_cache", "remote.buildbuddy.io", "Remote cache target, like remote.buildbuddy.io")
	resultsURL         = flag.String("results_url", "https://app.buildbuddy.io", "BuildBuddy results URL")
	invocationID       = flag.String("invocation_id", "", "Invocation ID to use (auto-generated if not specified)")
	remoteInstanceName = flag.String("remote_instance_name", "", "Cache namespace. Generally should be left unset.")
)

func EnableBES() bool {
	return *enableBES
}

func EnableCache() bool {
	return *enableCache
}

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

func BytestreamURIPrefix() string {
	backendURL, err := url.Parse(RemoteCache())
	if err != nil {
		return ""
	}
	return "bytestream://" + backendURL.Host
}
