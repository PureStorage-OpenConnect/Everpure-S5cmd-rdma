//go:build rdma && linux

package rdma

/*
#cgo linux CXXFLAGS: -std=c++17 -I/usr/local/cuda/targets/x86_64-linux/include
#cgo linux LDFLAGS: -L/usr/local/cuda/targets/x86_64-linux/lib -lcuobjclient -lcufile -lstdc++ -ldl -lrt -lpthread
#include <stdlib.h>
#include <string.h>
#include "rdma_shim.h"
*/
import "C"

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

const UseRDMAEnv = "S5CMD_USE_RDMA"

type Operation int

const (
	OperationGet Operation = iota
	OperationPut
)

var (
	sharedCuobjClientOnce sync.Once
	sharedCuobjClient     unsafe.Pointer
	sharedCuobjClientErr  error

	closeOnce sync.Once
)

// getSharedCuobjClient creates the process-wide client on first use and caches
// any creation error. cuObject allows concurrent use of the shared handle.
func getSharedCuobjClient() (unsafe.Pointer, error) {
	sharedCuobjClientOnce.Do(func() {
		h := C.s5cmd_rdma_client_new()
		if h == nil {
			sharedCuobjClientErr = fmt.Errorf("create RDMA client: %s", C.GoString(C.s5cmd_rdma_error_string(C.S5CMD_RDMA_ERR_CREATE)))
			return
		}
		sharedCuobjClient = h
	})
	return sharedCuobjClient, sharedCuobjClientErr
}

// CloseShared deletes the process-lifetime cuObjClient handle, if one was
// ever constructed. Safe to call even if RDMA was never used or construction
// failed. Call once, at process shutdown, after all transfers have finished.
func CloseShared() error {
	closeOnce.Do(func() {
		if sharedCuobjClient != nil {
			C.s5cmd_rdma_client_delete(sharedCuobjClient)
			sharedCuobjClient = nil
		}
	})
	return nil
}

// TransferObj owns one registered RDMA buffer borrowed by cuObject.
//
// PUT uses the caller's Go slice, pinned in place while cuObject reads it.
// GET uses native memory allocated by Go via C.malloc, because the endpoint
// needs a writable buffer before the response body exists.
// Memory ownership:
//
//   - PUT starts with bytes already owned by Go. The implementation pins the Go
//     slice, registers it, lets cuObject borrow the pointer while the request is
//     in flight, then deregisters and unpins it.
//   - GET starts before bytes exist locally. The implementation allocates a
//     native buffer from Go code with C.malloc, registers it, lets cuObject write
//     into it, then wraps the same buffer as the response body. Closing the
//     response body frees that native buffer.
//
// In both directions, cuObject borrows registered memory only for the transfer;
// s5cmd owns the memory lifecycle.
type TransferObj struct {
	handle     unsafe.Pointer
	buffer     unsafe.Pointer
	size       int
	token      string
	registered bool
	pinned     bool
	pinner     runtime.Pinner
	// ownsBufferCleanup tracks whether Close must clean up the buffer lifecycle:
	// unpin pinned Go memory for PUT, or free native memory for GET.
	// ReleaseReader clears this after handing GET buffer cleanup to the
	// response body reader.
	ownsBufferCleanup bool
}

type nativeBufferReader struct {
	buffer unsafe.Pointer
	data   []byte
	offset int
}

// Enabled reports whether RDMA was requested through the environment.
func Enabled() bool { return envEnabled(os.Getenv(UseRDMAEnv)) }

func newPutTransferObj(body []byte) (*TransferObj, error) {
	transfer, err := newTransferObj(len(body))
	if err != nil {
		return nil, err
	}

	if err := transfer.bindPutBuffer(body); err != nil {
		transfer.Close()
		return nil, err
	}

	if err := transfer.prepareToken(OperationPut); err != nil {
		transfer.Close()
		return nil, err
	}

	return transfer, nil
}

// newGetTransferObj allocates the destination buffer before the HTTP response
// arrives, because the server writes GET data directly into the registered
// RDMA buffer.
func newGetTransferObj(size int) (*TransferObj, error) {
	transfer, err := newTransferObj(size)
	if err != nil {
		return nil, err
	}

	buffer := C.malloc(C.size_t(size))
	if buffer == nil {
		transfer.Close()
		return nil, fmt.Errorf("allocate RDMA buffer: out of memory")
	}
	transfer.buffer = buffer
	transfer.ownsBufferCleanup = true

	if err := transfer.registerBuffer(); err != nil {
		transfer.Close()
		return nil, err
	}

	if err := transfer.prepareToken(OperationGet); err != nil {
		transfer.Close()
		return nil, err
	}

	return transfer, nil
}

func checkSize(size int) error {
	if size <= 0 {
		return fmt.Errorf("RDMA requires a positive transfer size")
	}

	maxSize := uint64(C.s5cmd_rdma_max_registration_size())
	if uint64(size) > maxSize {
		return fmt.Errorf("RDMA transfer size %d exceeds max size %d", size, maxSize)
	}

	return nil
}

func newTransferObj(size int) (*TransferObj, error) {
	if err := checkSize(size); err != nil {
		return nil, err
	}

	handle, err := getSharedCuobjClient()
	if err != nil {
		return nil, err
	}

	return &TransferObj{handle: handle, size: size}, nil
}

// bindPutBuffer registers body in place as the RDMA buffer, pinning it so the Go
// garbage collector cannot move it while cuObject borrows the pointer. This
// avoids an extra payload copy because the upload data is already in Go memory.
func (t *TransferObj) bindPutBuffer(body []byte) error {
	if len(body) != t.size {
		return fmt.Errorf("RDMA buffer size mismatch: got %d bytes, want %d", len(body), t.size)
	}
	if len(body) == 0 {
		return nil
	}

	t.pinner.Pin(&body[0])
	t.buffer = unsafe.Pointer(&body[0])
	t.pinned = true
	t.ownsBufferCleanup = true

	return t.registerBuffer()
}

// prepareToken generates an RDMA token for wire header exchanges.
func (t *TransferObj) prepareToken(operation Operation) error {
	if t.handle == nil || t.buffer == nil || !t.registered {
		return fmt.Errorf("RDMA transfer is not ready")
	}

	res := C.s5cmd_rdma_token(t.handle, t.buffer, C.size_t(t.size), C.int(operation))
	if res.code != C.S5CMD_RDMA_OK {
		return fmt.Errorf("prepare RDMA token: %s", C.GoString(C.s5cmd_rdma_error_string(res.code)))
	}
	defer C.s5cmd_rdma_free_token(t.handle, res.token)

	t.token = C.GoString(res.token)
	return nil
}

// Token returns the value to supply in the x-amz-rdma-token header.
func (t *TransferObj) Token() string {
	return t.token
}

// ReleaseReader wraps a completed GET buffer as the response body. The buffer
// was allocated by Go via C.malloc, borrowed by cuObject during the transfer,
// and is handed to the reader so it can be freed when the caller closes
// out.Body.
//
// An invalid size or transfer state returns an error, so callers cannot
// report a content length that differs from the readable buffer.
//
// This must never be called on a pinned PUT buffer: the reader frees with
// C.free on Close, which would corrupt the heap if pointed at Go-managed
// memory. Only GET transfers hand native buffers to readers.
func (t *TransferObj) ReleaseReader(size int) (io.ReadCloser, error) {
	if !t.ownsBufferCleanup || t.pinned {
		return nil, fmt.Errorf("RDMA GET buffer is not available for release")
	}
	if size < 0 || size > t.size || t.buffer == nil {
		return nil, fmt.Errorf("RDMA GET transferred size %d is invalid for buffer size %d", size, t.size)
	}

	data := unsafe.Slice((*byte)(t.buffer), size)
	buffer := t.buffer
	t.ownsBufferCleanup = false

	return &nativeBufferReader{
		buffer: buffer,
		data:   data,
	}, nil
}

// Close deregisters this transfer's buffer against the shared handle and
// releases the buffer lifecycle still owned by the transfer. It never deletes
// the shared handle itself — that happens once at process shutdown via
// CloseShared. Safe for repeated invocations.
func (t *TransferObj) Close() error {
	var err error

	if t.handle != nil && t.buffer != nil && t.registered {
		if code := C.s5cmd_rdma_deregister(t.handle, t.buffer); code != C.S5CMD_RDMA_OK {
			err = fmt.Errorf("deregister RDMA buffer: %s", C.GoString(C.s5cmd_rdma_error_string(code)))
		}
		t.registered = false
	}

	// The shared handle is intentionally not deleted here.
	t.handle = nil

	if t.ownsBufferCleanup {
		if t.pinned {
			t.pinner.Unpin()
		} else {
			t.freeBuffer()
		}
	}

	return err
}

func (t *TransferObj) registerBuffer() error {
	if code := C.s5cmd_rdma_register(t.handle, t.buffer, C.size_t(t.size)); code != C.S5CMD_RDMA_OK {
		return fmt.Errorf("register RDMA buffer: %s", C.GoString(C.s5cmd_rdma_error_string(code)))
	}
	t.registered = true
	return nil
}

func (t *TransferObj) freeBuffer() {
	if t.buffer != nil {
		C.free(t.buffer)
		t.buffer = nil
	}
}

// Read implements io.Reader over the C buffer slice.
func (r *nativeBufferReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}

	n := copy(p, r.data[r.offset:])
	r.offset += n
	if r.offset >= len(r.data) {
		return n, io.EOF
	}
	return n, nil
}

// Close frees the backing C allocation once reader processing finishes.
func (r *nativeBufferReader) Close() error {
	if r.buffer != nil {
		C.free(r.buffer)
		r.buffer = nil
		r.data = nil
	}
	return nil
}

func init() {
	if Enabled() {
		fmt.Fprintf(os.Stderr, "s5cmd: RDMA acceleration requested via %s=1\n", UseRDMAEnv)
	}
}
