//go:build rdma && linux

// C interface to NVIDIA's cuObject client, implemented in rdma_shim.cc.
//
// The shim exists so the Go side never sees the cuObject C++ API: cgo cannot
// call C++ directly, and keeping the C++ types out of the Go build also keeps
// the CUDA headers out of every file that imports this package. Errors are
// reduced to the integer codes below rather than propagating C++ exceptions or
// cuObject status types across the boundary.

#ifndef S5CMD_RDMA_SHIM_H
#define S5CMD_RDMA_SHIM_H

#include <stddef.h>

#ifdef __cplusplus
// The definitions in rdma_shim.cc mark every one of these noexcept; the
// exception specifier is part of a function's type in C++, so it must match
// here too or the two declarations conflict.
#define S5CMD_RDMA_NOEXCEPT noexcept
extern "C" {
#else
#define S5CMD_RDMA_NOEXCEPT
#endif

// Error codes returned by the functions below. Kept as plain ints so cgo can
// compare them directly; map to text with s5cmd_rdma_error_string.
enum {
	S5CMD_RDMA_OK = 0,
	S5CMD_RDMA_ERR_CREATE = 1,
	S5CMD_RDMA_ERR_REGISTER = 2,
	S5CMD_RDMA_ERR_DEREGISTER = 3,
	S5CMD_RDMA_ERR_TOKEN = 4,
	S5CMD_RDMA_ERR_INVALID = 5,
};

// Result of a token request. On success code is S5CMD_RDMA_OK and token is a
// cuObject-owned string that must be released with s5cmd_rdma_free_token;
// otherwise token is NULL.
typedef struct s5cmd_rdma_token_result {
	int code;
	char *token;
} s5cmd_rdma_token_result;

// Creates a connected cuObject client, or returns NULL if allocation or
// connection fails. The opaque handle is destroyed with s5cmd_rdma_client_delete.
void *s5cmd_rdma_client_new(void) S5CMD_RDMA_NOEXCEPT;
void s5cmd_rdma_client_delete(void *handle) S5CMD_RDMA_NOEXCEPT;
int s5cmd_rdma_register(void *handle, void *buffer, size_t size) S5CMD_RDMA_NOEXCEPT;
int s5cmd_rdma_deregister(void *handle, void *buffer) S5CMD_RDMA_NOEXCEPT;
// Mints a token for a registered buffer. operation is an rdma.Operation value:
// 0 for GET, 1 for PUT.
s5cmd_rdma_token_result s5cmd_rdma_token(void *handle, void *buffer, size_t size, int operation) S5CMD_RDMA_NOEXCEPT;
int s5cmd_rdma_free_token(void *handle, char *token) S5CMD_RDMA_NOEXCEPT;
// Largest buffer cuObject will register, used to reject oversized transfers
// before allocating.
size_t s5cmd_rdma_max_registration_size(void) S5CMD_RDMA_NOEXCEPT;
const char *s5cmd_rdma_error_string(int code) S5CMD_RDMA_NOEXCEPT;

#ifdef __cplusplus
}
#endif

#endif
