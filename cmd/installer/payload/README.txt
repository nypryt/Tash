# Installer payload

`go build` for the installer embeds every file in this directory. The release
build scripts copy the freshly built `tash` executable here as `tash_payload`
before building each platform installer, then remove it again.

Keeping only this README in git means `go build ./...` works in a fresh clone.
