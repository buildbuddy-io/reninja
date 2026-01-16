package remote_headers

import (
	"flag"
	"strings"

	"github.com/buildbuddy-io/reninja/internal/util"
)

var (
	remoteHeaders util.StringList
)

func init() {
	flag.Var(&remoteHeaders, "remote_header", "A set of remote headers to append to outgoing RPC requests.")
}

func GetPairs() []string {
	pairs := make([]string, 0)
	for _, header := range remoteHeaders {
		name, value, ok := strings.Cut(header, "=")
		if !ok {
			util.Warningf("Ignoring malformed header %q (format is 'k=v')", header)
			continue
		}
		pairs = append(pairs, name)
		pairs = append(pairs, value)
	}
	return pairs
}
