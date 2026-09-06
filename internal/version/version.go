// Package version is stamped at build time via -ldflags.
package version

// Version is the semantic version or a dev marker.
var Version = "dev"

// APIVersion is the daemon HTTP API major version; clients refuse a mismatch.
const APIVersion = 1
