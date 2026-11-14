package edbe

import "embed"

// the engine for this platform, put here by ./build.sh release
//
//go:embed all:lib/linux_arm64
var embedded embed.FS
