module pos-system

// The local agent is pinned to Go 1.20, the last toolchain that produces
// Windows 7 compatible binaries. Release builds MUST use a Go 1.20.x toolchain;
// Go 1.21+ dropped the Windows 7 runtime. Keep all code and dependencies
// pure-Go (no cgo) so cross-compilation stays trivial. The hosted cloud backend
// lives in the separate cloud/ module (modern Go + Postgres); the two are tied
// together by the workspace go.work but never share a toolchain.
go 1.20

require (
	github.com/google/uuid v1.6.0
	go.etcd.io/bbolt v1.3.9
)

require golang.org/x/sys v0.4.0 // indirect
