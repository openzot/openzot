# Bundled UI dependencies

These files are embedded in the `zotui` binary. Refresh them with
`make vendor-ui`; do not edit generated modules or font files by hand.

- `@wterm/dom` and `@wterm/core` 0.3.2, Apache-2.0
- Fontsource DotGothic16 5.3.0, OFL-1.1
- Fontsource IBM Plex Mono 5.3.0, OFL-1.1

The download script pins the SHA-256 of every upstream npm tarball. Updating a
dependency therefore requires changing both its version and expected checksum.
Font binaries use the repository's existing Git LFS rules for `.woff2` files.
