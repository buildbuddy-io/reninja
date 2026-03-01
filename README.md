# Reninja

## About

Reninja is a complete reimplementation of the Ninja build system
focused on correctness, performance, and remote caching/execution.

Reninja owes its existence to the original [Ninja Build
System](https://ninja-build.org/) and all credit goes to the [original
author](https://neugierig.org/) and [many open source
contributors](https://github.com/ninja-build/ninja/graphs/contributors).
All bugs / mistakes are my own.

You can read about how Reninja was developed
[here](https://www.buildbuddy.io/blog/).

## Quick Start
```bash
  ## Install the Reninja binary:
  $ go install github.com/buildbuddy-io/reninja/cmd/reninja

  ## Build as normal (does the exact same thing as ninja)
  $ ninja

  ## Build with Build Event Stream (BES) enabled
  $ ninja --bes_backend=remote.buildbuddy.io

  ## Build your project with BES and Remote Cache enabled
  $ ninja --bes_backend=remote.buildbuddy.io --remote_cache=remote.buildbuddy.io

  ## Build with remote execution (see below for details)
  $ docker run --rm 
```

## Motivation

Ninja is an excellent and **simple** build tool. Many projects have
modified it to add in various forms of observability or remote
execution. I wanted to roll up some of those improvements in one
place and also add proper support for *remote caching* and *remote
execution*, which do not cleanly fit in the original project due to complex
networking requirements and extensive (proto, gRPC) dependencies.

At BuildBuddy, we've spent a lot of time building tools for
[Bazel](https://bazel.build/) and our customers derive a lot of value
from being able to build and test their software in a remote execution
environment or with more observability.

I wanted to make those features available to projects that can build with Ninja
and offer Reninja as a simple, generic replacement for distcc-style builds.

## Features
 - **Drop in replacement for Ninja** - If it works in Ninja, Reninja will build
 it too. By default, all flags and options are honored, even the hidden 🐢 ones.
 - **Build Visibility** - use the timing profile (flame graph) to visualize the slow
 parts of the build and fix them.
 - **Remote caching** - allows for massive reductions in CPU usage and drastically
 reduces build times by not building the same thing twice.
 - **Remote execution** - run builds with massive parallelization (-j 2000) to
 speed them up. Build LLVM from scratch in 3 minutes!
 - **Extensive unit and integration tests** - all Ninja tests were ported over, and
 new ones added for Reninja-only functionality. Additional parity tests ensure
 that Reninja and Ninja produce the same outputs.

## Installation
 
