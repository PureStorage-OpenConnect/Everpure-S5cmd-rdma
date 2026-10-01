//go:build !rdma || !linux

// Package rdma provides optional RDMA acceleration for S3 transfers against
// endpoints that understand the x-amz-rdma-token header.
//
// The package has two implementations selected by build tag. This file is the
// stub used by default builds: it keeps the API available so callers need no
// build tags of their own, but every transfer operation fails with
// ErrNotBuilt. rdma_impl.go holds the real cgo implementation, compiled only
// with "-tags rdma" on Linux. Keeping the stub dependency-free is what lets
// the default build work without CUDA, GDS, or cuObject installed.
package rdma

import (
	"io"
	"os"
)

// UseRDMAEnv is the environment variable that requests RDMA at runtime.
const UseRDMAEnv = "S5CMD_USE_RDMA"

// Operation defines transfer direction types.
type Operation int

const (
	OperationGet Operation = iota
	OperationPut
)

// TransferObj is a stub type used when s5cmd is built without RDMA support.
type TransferObj struct{}

// Enabled reports whether RDMA was requested through the environment.
func Enabled() bool { return envEnabled(os.Getenv(UseRDMAEnv)) }

func newPutTransferObj(body []byte) (*TransferObj, error) { return nil, ErrNotBuilt }
func newGetTransferObj(size int) (*TransferObj, error)    { return nil, ErrNotBuilt }

func (t *TransferObj) Token() string { return "" }

// ReleaseReader cannot release a buffer in the stub build.
func (t *TransferObj) ReleaseReader(size int) (io.ReadCloser, error) {
	return nil, ErrNotBuilt
}

func (t *TransferObj) Close() error { return nil }

// CloseShared is a no-op in the stub build; there is no shared handle to
// release since RDMA is never built in.
func CloseShared() error { return nil }
