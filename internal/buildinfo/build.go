// Package buildinfo reports which kind of binary this is.
//
// Named buildinfo rather than build because a bare `build` is caught by the
// monorepo's root .gitignore, which excludes any directory of that name
// anywhere in the tree. The package compiled and tested perfectly while git
// silently refused to track it, and the failure surfaced only in CI, as an
// import that could not be resolved.
//
// zot runs unattended with a provider key and a shell tool, so the difference
// between a developer's build and a released one is a security boundary, not a
// convenience. The one thing that currently turns on it is whether a `.env` in
// the working directory is read: handy while developing, and unacceptable in a
// binary someone downloads, because it means pointing zot at a directory is
// enough to load credentials out of it - a repository you cloned to review, a
// shared checkout, anything with a stray `.env` a colleague committed.
//
// The switch is a build tag rather than a version string, and it defaults to
// off. That direction is deliberate: a release built without the ldflags that
// stamp a version would still look like a release, but a build that forgets
// `-tags dev` merely loses a developer convenience. The failure mode of
// forgetting has to be the safe one.
package buildinfo
