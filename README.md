# Reninja

```
 ____  _____ _   _ ___ _   _     _   _
|  _ \| ____| \ | |_ _| \ | |   | | / \
| |_) |  _| |  \| || ||  \| |_  | |/ _ \
|  _ <| |___| |\  || || |\  | |_| / ___ \
|_| \_\_____|_| \_|___|_| \_|\___/_/   \_\
```

## About

Reninja is a complete reimplementation of the Ninja build system
focused on correctness, remote caching, remote execution, and build
telemetry.

Reninja owes its existence to the original [Ninja Build
System](https://ninja-build.org/) and all credit goes to the [original
author](https://neugierig.org/) and [many open source
contributors](https://github.com/ninja-build/ninja/graphs/contributors).
All bugs / mistakes are my own.

You can read about how Reninja was developed
[here](https://www.buildbuddy.io/blog/).

## Quick Start

#### Install the Reninja binary:
```bash
  $ go install github.com/buildbuddy-io/reninja/cmd/reninja@latest
```

#### Build locally 
  This does the exact same thing as ninja.
```bash
  $ reninja
```

#### Build with Build Event Stream (BES) enabled
```bash
  $ reninja --bes_backend=remote.buildbuddy.io
```

#### Build your project with BES and Remote Cache enabled
```bash
  $ reninja --bes_backend=remote.buildbuddy.io --remote_cache=remote.buildbuddy.io
```

#### Build with remote execution (see [Remote Execution](#remote-execution) below for details)
```bash
  $ SRC=$PWD
  $ BUILD_DIR=$SRC/build-rbe
  $ mkdir -p "$BUILD_DIR" \
  $ docker run --rm \
	  --user "$(id -u):$(id -g)" \
	  -v "$SRC:$SRC" \
	  -v "$(which ninja):/usr/local/bin/ninja:ro" \
	  -w "$BUILD_DIR" \
	  gcr.io/flame-public/rbe-ubuntu22-04:ninja \
	  cmake -G Ninja \
		-DCMAKE_SUPPRESS_REGENERATION=ON \
		"$SRC"
  $ reninja --bes_backend=remote.buildbuddy.io \
      --remote_cache=remote.buildbuddy.io \
	  --remote_executor=remote.buildbuddy.io \
	  --container_image=gcr.io/flame-public/rbe-ubuntu22-04:ninja \
	  --remote_header=x-buildbuddy-api-key=YOUR_API_KEY_HERE \
	  -j 1000
```

## Motivation

Ninja is an excellent and **simple** build tool. Many projects have
modified it to add in various forms of observability or remote
execution. I wanted to roll up some of those improvements in one place
and also add proper support for *remote caching* and *remote
execution*, which do not cleanly fit in the original project due to
complex networking requirements and extensive (proto, gRPC)
dependencies.

At BuildBuddy, we've spent a lot of time building tools for
[Bazel](https://bazel.build/) and our customers derive a lot of value
from being able to build and test their software in a remote execution
environment or with more observability.

I wanted to make those features available to ninja-based projects and
offer Reninja as a simple, generic replacement for distcc-style
building. There's nothing BuildBuddy specific here -- Reninja is just
a normal [remote-apis](https://github.com/bazelbuild/remote-apis/) client.

## Features
 - **Drop in replacement for Ninja** - If it works in Ninja, Reninja
 will build it too. By default, all flags and options are honored,
 even the hidden 🐢 ones.
 - **Build Visibility** - use the timing profile (flame graph) to
 visualize the slow parts of the build and fix them.
 - **Remote caching** - allows for massive reductions in CPU usage and
 drastically reduces build times by not building the same thing twice.
 - **Remote execution** - run builds with massive parallelization (-j
 2000) to speed them up. Build LLVM from scratch in 3 minutes!
 - **Extensive unit and integration tests** - all Ninja tests were
 ported over, and new ones added for Reninja-only
 functionality. Additional parity tests ensure that Reninja and Ninja
 produce the same outputs.

## Installation

Because Reninja is a golang application, you can install it with `go install`:
```bash
  $ go install github.com/buildbuddy-io/reninja/cmd/reninja@latest
```

We also offer prebuilt binaries for Linux and Mac attached to the github release:
```bash
  $ curl -fSL "https://github.com/buildbuddy-io/reninja/releases/latest/download/ninja-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed
  's/x86_64/amd64/;s/aarch64/arm64/').zip" -o ninja.zip
  $ unzip ninja.zip
  $ mv ninja /usr/local/bin/ninja
```

## NinjaRC (Config file configuration)

One powerful feature Reninja borrows from Bazel is the ability to read
config files from various locations that define common build
flags. This can be used to define a common build configuration (host,
remote namespace, container image, etc) that all builders of the
project should use.

Out of the box, Reninja will look for a file called `.ninjarc` in the
following places:
 - the CWD (`.ninjarc`)
 - the project root (`%workspace%/.ninjarc`)
 - the user's home directory (`~/.ninjarc`)
 - the system etc dir (`/etc/.ninjarc`)

A contrived, basic `.ninjarc` file might look like this:
```
build:local --bes_backend="grpc://localhost:1985"
build:local --remote_cache="grpc://localhost:1985"
build:local --remote_executor="grpc://localhost:1985"
build:local --results_url="http://localhost:8080"
```

This config specifies that for "build" commands, when the --config
flag value is "local", the `--bes_backend`, `--remote_cache` and
`--results_url` flags will be set.

A more useful example might look like this:
```
build:common --remote_header=x-buildbuddy-api-key=YOUR_API_KEY_HERE

build:bes --bes_backend=remote.buildbuddy.io 
build:bes --results_url=https://app.buildbuddy.io
build:cache --config=bes --remote_cache=remote.buildbuddy.io
build:remote --config=cache --remote_executor=remote.buildbuddy.io
build:remote --container_image="gcr.io/flame-public/rbe-ubuntu22-04:ninja"
build:remote -j 2000

# default to bes + caching, allow passing "--config=remote" to rexec
build --config=cache
```

This config defines three different modes `bes`, `cache`, and `remote`
and selects `cache` by default for ninja builds.

## Remote Execution

Remote execution with Reninja is more challenging than with Bazel
because build actions (edges, in ninja parlance) do not always fully
declare *all* of their inputs.  Additionally, CMake defaults to
configuring against the installed system libraries rather than
specifying everything at the project level (cmake toolchains are kind
of an option here, but not often used).

To sidestep these issues, remote execution with Reninja generally
requires two things: 1) configuring the build inside a container and
2) using include scanning to determine the inputs for an action.
 
This sounds more complex than it is. It also has the nice property
that once a project does this, all contributors to the project are
working with a commonly known set of tools -- everything is fully
declared either in the container image used for remote execution or in
the source code.

Here's an example of using ninja with remote execution to build duckdb
(a small to mid-size c++ project configured with cmake):

Clone the repo:
```bash
  $ git clone https://github.com/duckdb/duckdb.git
```

Configure it with cmake (against a docker image):
```bash
  $ docker run --rm \
      --user "$(id -u):$(id -g)" \
	  -v "/home/tylerw/duckdb:/home/tylerw/duckdb" \
	  -v "/home/tylerw/bin/ninja:/usr/local/bin/ninja:ro" \
	  -w "/home/tylerw/duckdb/build-rbe" \
	  gcr.io/flame-public/rbe-ubuntu22-04:ninja \
	  cmake -G Ninja -DCMAKE_SUPPRESS_REGENERATION=ON /home/tylerw/duckdb
```

Run the build using remote execution:
```bash
  $ reninja --bes_backend=remote.buildbuddy.io \
      --remote_cache=remote.buildbuddy.io \
	  --remote_executor=remote.buildbuddy.io \
	  --container_image=gcr.io/flame-public/rbe-ubuntu22-04:ninja \
	  --remote_header=x-buildbuddy-api-key=YOUR_API_KEY_HERE \
	  -j 1000
```

## Usage of AI

Is this just another AI slop project? **No!**

Reninja was born from a bet (could AI do this?) but since the [initial version](https://github.com/buildbuddy-io/reninja/commit/8c1bde042af17056246167a338b74aa2172b728c) I have re-written each file by hand in go. I have occasionally relied on claude to port unit tests, but only after writing several examples myself would I then ask it to port more tests following my lead.

Some other BES, remote caching, and remote execution libraries were borrowed from BuildBuddy and lightly modified to be suitable for Reninja.
