# Dr. Make

_The "Dr" stands for Docker_

Dr. Make is a [Make][make]-like tool that builds general purpose targets by
running commands inside isolated Docker containers, allowing you to (a) take
advantage of software from pre-built images without needing any build software
on your development machine and (b) gain full isolation in the state of a
build, allowing you to create more reliable build tools that work the same
regardless of the development machine they are run on.

## Installing

You can install with Go:

```sh
go install github.com/lsegal/drmake/cmd/drmake
```

Or download pre-built binaries provided in [Releases][releases].

## Usage

Create a `Makefile.phd` that executes commands:

```Dockerfile
FROM alpine AS print_version
LABEL Description="Prints the version that you give it"
ENVARG VERSION
CMD echo "The version is: ${VERSION}"

FROM alpine AS say_hello USING print_version
CMD echo "Wasn't that a nice version or what?"
```

The above `Makefile.phd` defines 2 targets, `print_version` and `say_hello`.
The first accepts an argument `VERSION` which can be passed via:

```sh
drmake print_version -a VERSION=1.2.3
```

The second target, `say_hello`, will echo some stuff after `print_version`,
its dependency, runs.

You can _list_ all targets by using `drmake -l`.

## Makefile.phd Syntax

`Makefile.phd`s (also known as Phdfiles, Drfiles, or Drakefiles) look a lot like
Dockerfile in that you define multiple `FROM` blocks each with the `AS name`
suffix to define "targets". These targets are what `drmake` will execute.
That said, Phdfiles also come with a few tiny differences:

### `FROM image USING dependencies...`

You can add `USING a b c d` to the end of a `FROM` line to define target
dependencies (the target would depend on targets a, b, c, d in that order).

For example:

```Dockerfile
FROM alpine AS install
RUN apk add -U git

FROM alpine AS clone USING install
RUN git clone git://github.com/lsegal/drmake

FROM golang:1-alpine AS test USING clone
RUN go test .
```

### `FROM ./path/to/directory`

You can use `FROM ./path/to/dir` syntax to point to a relative directory
that contains a Dockerfile (point at the dir, not the Dockerfile).

### `FROM #target`

You can use `FROM #target` to copy the Dockerfile instructions from another
target:

```Dockerfile
FROM golang:1-alpine AS base
ENV GOPATH=/root/go

FROM #base AS build_tools
CMD go build ./tools

FROM #base AS build_deps
CMD go build ./deps
```

### `FROM &target`

This syntax is shorthand for pulling Dockerfile instructions from a directory
relative to your workspace inside of `.drmake/targets/NAME`.

In other words, the following:

```Dockerfile
FROM &build
```

Is short-hand for:

```Dockerfile
FROM ./.drmake/targets/build as build
```

Note that another benefit of using `&target` syntax is that you may omit the
`AS build` part of the statement. The target name for both of these lines will
be `build`.

### `ENVARG ARGUMENT=VALUE`

You can use this syntax to quickly define a build argument that is defined
as an environment variable for later commands. This is a shortcut for:

```Dockerfile
ARG ARGUMENT=VALUE
ENV ARGUMENT=${ARGUMENT}
```

### `ARTIFACT src dst`

You can use this in any target to define an artifact file that should be copied
out of the image back to the host volume, for example:

```Dockerfile
ARTIFACT build/* build/
```

The above line copies all files inside the `build` directory of the isolated
volume back to the host volume under a `build/` directory.

You can copy individual files or directories; semantics work similarly to
running `cp -R` with the src and dst arguments.

## TODO

- [ ] Support targets sourced from other Git repos (`https://` & `git://`)
- [ ] Better parsing and error messages
- [ ] Tests
- [ ] Possibly a whole new syntax closer to [GitHub Actions][actions]?

## Copyright & License

DrMake is copyright © 2019 by Loren Segal and licensed under the MIT license.

[make]: https://www.gnu.org/software/make/
[releases]: https://github.com/lsegal/drmake/releases
[actions]: https://developer.github.com/actions/managing-workflows/workflow-configuration-options/
