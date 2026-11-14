//go:build !((linux && amd64) || (linux && arm64) || (darwin && amd64) || (darwin && arm64))

package edbe

import "embed"

// no engine for this platform: Open says so
var embedded embed.FS
