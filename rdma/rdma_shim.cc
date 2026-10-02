//go:build rdma && linux

// Implementation of the C shim declared in rdma_shim.h. This is the only file
// that includes cuObject's C++ headers; everything crossing to Go goes through
// extern "C" with opaque handles and integer error codes.
//
// Note: cgo ignores the //go:build line above in C/C++ sources. This file is
// compiled whenever the package is built, and the "rdma" tag matters only
// because rdma_impl.go (which pulls in this directory's cgo directives) is
// itself tag-gated.

#include "rdma_shim.h"

#include <cstddef>
#include <iostream>

#include "cuobjclient.h"

// Check the version when cuobjclient.h provides it, during build time.
#if defined(CUOBJ_CLIENT_MAJOR_VERSION) && defined(CUOBJ_CLIENT_MINOR_VERSION) && \
    (CUOBJ_CLIENT_MAJOR_VERSION < 1 || \
     (CUOBJ_CLIENT_MAJOR_VERSION == 1 && CUOBJ_CLIENT_MINOR_VERSION < 3))
#error "s5cmd RDMA requires cuObjClient 1.3 or newer"
#endif

namespace {

// Every function below is noexcept: an exception unwinding across the extern
// "C" boundary into Go is undefined behavior, so cuObject failures must
// surface as status codes, never as C++ exceptions.

// handle is always a cuObjClient* smuggled through Go as a void*; centralizing
// the cast keeps that assumption in one place instead of one per function.
inline cuObjClient* to_client(void* handle) noexcept {
    return reinterpret_cast<cuObjClient*>(handle);
}

// Maps integer operation codes sent from Go (rdma_impl.go) to cuObjOpType_t.
bool parse_op(int operation, cuObjOpType_t* out_op) noexcept {
    switch (operation) {
    case 0: // OperationGet
        *out_op = CUOBJ_GET;
        return true;
    case 1: // OperationPut
        *out_op = CUOBJ_PUT;
        return true;
    default:
        return false;
    }
}

} // namespace

extern "C" {

void* s5cmd_rdma_client_new(void) noexcept {
    // Null get/put ops: s5cmd issues its own S3 HTTP requests and relies on cuObject
    // solely for memory buffer registration and RDMA token generation.
    CUObjIOOps ops{};
    ops.get = nullptr;
    ops.put = nullptr;

    // std::nothrow instead of a try/catch: this function is noexcept, so a
    // throwing new that failed would terminate the process rather than let us
    // report an allocation failure as nullptr.
    auto* client = new (std::nothrow) cuObjClient(ops, CUOBJ_PROTO_RDMA_DC_V1);
    if (!client) {
        return nullptr;
    }

    if (!client->isConnected()) {
        delete client;
        return nullptr;
    }

    return client;
}

void s5cmd_rdma_client_delete(void* handle) noexcept {
    if (handle != nullptr) {
        delete to_client(handle);
    }
}

int s5cmd_rdma_register(void* handle, void* buffer, size_t size) noexcept {
    if (handle == nullptr || buffer == nullptr || size == 0) {
        return S5CMD_RDMA_ERR_INVALID;
    }
    // Checked here rather than left to cuObject: an oversized request must
    // come back as S5CMD_RDMA_ERR_REGISTER, not whatever cuMemObjGetDescriptor
    // happens to do with a size past its limit.
    if (size > CUOBJ_MAX_MEMORY_REG_SIZE) {
        return S5CMD_RDMA_ERR_REGISTER;
    }

    auto status = to_client(handle)->cuMemObjGetDescriptor(buffer, size);
    return (status == CU_OBJ_SUCCESS) ? S5CMD_RDMA_OK : S5CMD_RDMA_ERR_REGISTER;
}

int s5cmd_rdma_deregister(void* handle, void* buffer) noexcept {
    if (handle == nullptr || buffer == nullptr) {
        return S5CMD_RDMA_ERR_INVALID;
    }

    auto status = to_client(handle)->cuMemObjPutDescriptor(buffer);
    return (status == CU_OBJ_SUCCESS) ? S5CMD_RDMA_OK : S5CMD_RDMA_ERR_DEREGISTER;
}

s5cmd_rdma_token_result s5cmd_rdma_token(void* handle, void* buffer, size_t size, int operation) noexcept {
    if (handle == nullptr || buffer == nullptr || size == 0) {
        return {S5CMD_RDMA_ERR_INVALID, nullptr};
    }

    cuObjOpType_t op;
    if (!parse_op(operation, &op)) {
        return {S5CMD_RDMA_ERR_INVALID, nullptr};
    }

    char* token = nullptr;
    auto status = to_client(handle)->cuMemObjGetRDMAToken(buffer, size, 0, op, &token);
    if (status != CU_OBJ_SUCCESS) {
        return {S5CMD_RDMA_ERR_TOKEN, nullptr};
    }

    return {S5CMD_RDMA_OK, token};
}

int s5cmd_rdma_free_token(void* handle, char* token) noexcept {
    // Treat null pointers as success so Go caller defers can execute unconditionally.
    if (handle == nullptr || token == nullptr) {
        return S5CMD_RDMA_OK;
    }

    auto status = to_client(handle)->cuMemObjPutRDMAToken(token);
    return (status == CU_OBJ_SUCCESS) ? S5CMD_RDMA_OK : S5CMD_RDMA_ERR_TOKEN;
}

size_t s5cmd_rdma_max_registration_size(void) noexcept {
    return CUOBJ_MAX_MEMORY_REG_SIZE;
}

const char* s5cmd_rdma_error_string(int code) noexcept {
    switch (code) {
    case S5CMD_RDMA_OK:
        return "success";
    case S5CMD_RDMA_ERR_CREATE:
        return "cuobjclient creation failed";
    case S5CMD_RDMA_ERR_REGISTER:
        return "cuobjclient buffer registration failed";
    case S5CMD_RDMA_ERR_DEREGISTER:
        return "cuobjclient buffer deregistration failed";
    case S5CMD_RDMA_ERR_TOKEN:
        return "cuobjclient RDMA token operation failed";
    case S5CMD_RDMA_ERR_INVALID:
        return "invalid RDMA argument";
    default:
        return "unknown RDMA error";
    }
}

} // extern "C"
