package rdma

import (
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// RDMA wire protocol constants: header names and control values exchanged with RDMA endpoints.
const (
	TokenHeader            = "x-amz-rdma-token"
	ReplyHeader            = "x-amz-rdma-reply"
	BytesTransferredHeader = "x-amz-rdma-bytes-transferred"
	UnsupportedReply       = "501"

	contentLengthHeader = "Content-Length"
	emptyContentLength  = "0"
)

// ErrNotBuilt is returned by operations when compiled without "-tags rdma".
var ErrNotBuilt = errors.New("s5cmd was built without RDMA support")

// PreparePut registers a buffer containing body and returns a PUT token.
func PreparePut(body []byte) (*TransferObj, error) {
	return newPutTransferObj(body)
}

// PreparePutWithMD5 is PreparePut, plus it computes the Content-MD5 value
// for body concurrently with the RDMA transfer setup and token
// mint, overlapping two independent costs that together dominate a small
// object's RDMA PUT latency. Pass computeMD5 as false to skip the hash
// entirely (for example when S3DisableContentMD5Validation is set).
func PreparePutWithMD5(body []byte, computeMD5 bool) (transfer *TransferObj, md5Value string, err error) {
	var wg sync.WaitGroup
	if computeMD5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			md5Value = ContentMD5(body)
		}()
	}

	transfer, err = PreparePut(body)

	wg.Wait()

	if err != nil {
		return nil, "", err
	}
	return transfer, md5Value, nil
}

// PrepareGet registers an empty buffer of the given size and returns a GET token.
func PrepareGet(size int) (*TransferObj, error) {
	return newGetTransferObj(size)
}

// ContentMD5 calculates the Base64-encoded MD5 hash for the given payload.
func ContentMD5(body []byte) string {
	hash := md5.Sum(body)
	return base64.StdEncoding.EncodeToString(hash[:])
}

// ReadAll buffers an entire io.Reader payload into memory.
// For Seekers, it measures available stream length upfront to avoid reallocations.
func ReadAll(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, nil
	}

	seeker, isSeeker := body.(io.Seeker)
	if !isSeeker {
		return io.ReadAll(body)
	}

	size, err := seekerRemainingSize(seeker)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return []byte{}, nil
	}

	buffer := make([]byte, size)
	n, err := io.ReadFull(body, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buffer[:n], nil
}

// seekerRemainingSize evaluates the unread byte length of an io.Seeker and restores its original offset.
func seekerRemainingSize(seeker io.Seeker) (int64, error) {
	current, err := seeker.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}

	end, err := seeker.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}

	// Always restore offset before returning
	_, restoreErr := seeker.Seek(current, io.SeekStart)
	if restoreErr != nil {
		return 0, restoreErr
	}

	if end < current {
		return 0, fmt.Errorf("reader offset %d is past end %d", current, end)
	}

	size := end - current
	return size, nil
}

// ParseBoundedRangeSize extracts exact byte lengths from "bytes=start-end" HTTP headers.
func ParseBoundedRangeSize(rangeHeader string) (int, error) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, fmt.Errorf("missing bytes= prefix")
	}

	rangePart := strings.TrimPrefix(rangeHeader, "bytes=")
	dashIndex := strings.Index(rangePart, "-")
	if dashIndex <= 0 || dashIndex == len(rangePart)-1 {
		return 0, fmt.Errorf("range must include start and end offsets")
	}

	start, err := strconv.ParseInt(rangePart[:dashIndex], 10, 64)
	if err != nil || start < 0 {
		return 0, fmt.Errorf("invalid range start")
	}

	end, err := strconv.ParseInt(rangePart[dashIndex+1:], 10, 64)
	if err != nil || end < start {
		return 0, fmt.Errorf("invalid range end")
	}

	size := end - start + 1
	if size <= 0 {
		return 0, fmt.Errorf("range size is too large")
	}

	return int(size), nil
}

// SizeFromRange is a convenience boolean wrapper for ParseBoundedRangeSize.
func SizeFromRange(rangeHeader string) (int, bool) {
	size, err := ParseBoundedRangeSize(rangeHeader)
	return size, err == nil
}

// ShouldFallbackHeader checks if the remote server requested a plain TCP fallback.
func ShouldFallbackHeader(headers http.Header) bool {
	return headers.Get(ReplyHeader) == UnsupportedReply
}

// BytesTransferred parses byte counts returned in RDMA GET response headers.
func BytesTransferred(headers http.Header) (int, bool, error) {
	value := headers.Get(BytesTransferredHeader)
	if value == "" {
		return 0, false, nil
	}

	bytesTransferred, err := strconv.ParseInt(value, 10, 64)
	if err != nil || bytesTransferred < 0 {
		return 0, false, fmt.Errorf("invalid %s header: %q", BytesTransferredHeader, value)
	}
	return int(bytesTransferred), true, nil
}

// RequiredBytesTransferred parses the byte count required on successful RDMA
// GET responses.
func RequiredBytesTransferred(headers http.Header) (int, error) {
	bytesTransferred, ok, err := BytesTransferred(headers)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("missing %s header", BytesTransferredHeader)
	}
	return bytesTransferred, nil
}

// PutHeaders returns HTTP headers for RDMA PUT/UploadPart operations.
func PutHeaders(token string) map[string]string {
	return map[string]string{
		TokenHeader:         token,
		contentLengthHeader: emptyContentLength,
	}
}

// GetHeaders returns HTTP headers for RDMA GET operations.
func GetHeaders(token string) map[string]string {
	return map[string]string{
		TokenHeader: token,
	}
}

// envEnabled parses a boolean environment string, returning false on error.
func envEnabled(value string) bool {
	enabled, err := strconv.ParseBool(value)
	return err == nil && enabled
}
