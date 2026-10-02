# RDMA Support

This package contains optional RDMA acceleration for S3-compatible endpoints
that support the s5cmd RDMA headers.

## Build

RDMA support is disabled in normal builds. A default build does not require
CUDA, GDS, cuObject, CGO, or RDMA libraries.

Build with RDMA support using the `rdma` build tag:

```sh
make build-rdma
```

The RDMA build requires:

- Go 1.21+ with CGO support
- CUDA/cuObject headers and libraries
- `libcuobjclient`
- `libcufile`

The shim targets the cuObjClient 1.3 API. Use matching cuObjClient headers and
libraries. Headers that expose version macros are checked at build time for
version 1.3 or newer. Newer major versions can build if they provide the API
used by the shim. Older headers without the macros cannot be checked
automatically.

If CUDA is installed in a non-standard location, set `CUDA_PATH` before
building.

## Runtime

RDMA is opt-in at runtime:

```sh
S5CMD_USE_RDMA=1
```

If this variable is not set, s5cmd uses the normal TCP path.

If this variable is set on a binary that was not built with RDMA support, s5cmd
returns an error instead of silently falling back to TCP.

## Supported Operations

RDMA is attempted for:

- bounded ranged GET requests
- multipart upload parts
- single-part uploads

Empty uploads use the normal TCP path.

## Protocol Headers

RDMA requests send:

```http
x-amz-rdma-token: <token>
```

RDMA upload requests send no HTTP body and use:

```http
Content-Length: 0
```

An endpoint can decline RDMA for a request with:

```http
x-amz-rdma-reply: 501
```

For upload requests, s5cmd retries the same request over TCP. For GET requests,
the same response carries the normal HTTP body and no second request is sent.

Successful RDMA GET responses must include:

```http
x-amz-rdma-bytes-transferred: <n>
```

This value is used as the response content length.

## Behavior

- RDMA is used only when explicitly requested with `S5CMD_USE_RDMA`.
- Server-declined RDMA uploads retry over TCP.
- Server-declined RDMA GETs use the HTTP response body already returned by the
  server.
- Local RDMA setup failures are returned as errors.
- Successful RDMA GET responses without a transferred-byte count are treated as
  protocol errors.
- Upload checksums are still computed from the original payload when SDK MD5
  validation is enabled.
