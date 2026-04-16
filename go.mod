// go.mod
module github.com/tbox-run/tbox

go 1.25.0

require (
	github.com/gofrs/flock v0.8.1
	github.com/spf13/cobra v1.10.2
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/sys v0.43.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

// No CGO dependencies — pure Go for cross-compile compatibility
// NOTE: For DNS to work on Android, build natively in Termux (CGO enabled by default)
