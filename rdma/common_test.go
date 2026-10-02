package rdma

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func TestEnabled(t *testing.T) {
	t.Setenv(UseRDMAEnv, "")
	if Enabled() {
		t.Fatal("Enabled() = true, want false")
	}

	t.Setenv(UseRDMAEnv, "true")
	if !Enabled() {
		t.Fatal("Enabled() = false, want true")
	}

	t.Setenv(UseRDMAEnv, "1")
	if !Enabled() {
		t.Fatal("Enabled() = false for 1, want true")
	}

	t.Setenv(UseRDMAEnv, "false")
	if Enabled() {
		t.Fatal("Enabled() = true for false env value, want false")
	}
}

func TestSizeFromRange(t *testing.T) {
	tests := []struct {
		name        string
		rangeHeader string
		wantSize    int
		wantOK      bool
	}{
		{
			name:        "bounded range",
			rangeHeader: "bytes=10-19",
			wantSize:    10,
			wantOK:      true,
		},
		{
			name:        "missing end",
			rangeHeader: "bytes=10-",
			wantOK:      false,
		},
		{
			name:        "missing prefix",
			rangeHeader: "10-19",
			wantOK:      false,
		},
		{
			name:        "end before start",
			rangeHeader: "bytes=19-10",
			wantOK:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSize, gotOK := SizeFromRange(tt.rangeHeader)
			if gotSize != tt.wantSize || gotOK != tt.wantOK {
				t.Fatalf("SizeFromRange(%q) = (%d, %v), want (%d, %v)", tt.rangeHeader, gotSize, gotOK, tt.wantSize, tt.wantOK)
			}
		})
	}
}

func TestParseBoundedRangeSize(t *testing.T) {
	tests := []struct {
		name        string
		rangeHeader string
		wantSize    int
		wantErr     bool
	}{
		{
			name:        "bounded range",
			rangeHeader: "bytes=10-19",
			wantSize:    10,
		},
		{
			name:        "missing end",
			rangeHeader: "bytes=10-",
			wantErr:     true,
		},
		{
			name:        "missing prefix",
			rangeHeader: "10-19",
			wantErr:     true,
		},
		{
			name:        "end before start",
			rangeHeader: "bytes=19-10",
			wantErr:     true,
		},
		{
			name:        "range size overflows int64",
			rangeHeader: "bytes=0-9223372036854775807",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSize, err := ParseBoundedRangeSize(tt.rangeHeader)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseBoundedRangeSize(%q) succeeded, want error", tt.rangeHeader)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBoundedRangeSize(%q) failed: %v", tt.rangeHeader, err)
			}
			if gotSize != tt.wantSize {
				t.Fatalf("ParseBoundedRangeSize(%q) = %d, want %d", tt.rangeHeader, gotSize, tt.wantSize)
			}
		})
	}
}

func TestReadAllReadsSeekableReaderFromOriginalOffset(t *testing.T) {
	reader := bytes.NewReader([]byte("0123456789"))
	if _, err := reader.Seek(4, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	got, err := ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "456789" {
		t.Fatalf("ReadAll() = %q, want %q", got, "456789")
	}

	position, err := reader.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if position != 10 {
		t.Fatalf("reader position = %d, want 10", position)
	}
}

// TestWireConstants pins the on-the-wire header names and values. These form a
// protocol contract with the endpoint, so a rename must be a deliberate change
// rather than an incidental refactor.
func TestWireConstants(t *testing.T) {
	constants := map[string]string{
		"TokenHeader":            TokenHeader,
		"ReplyHeader":            ReplyHeader,
		"BytesTransferredHeader": BytesTransferredHeader,
		"UnsupportedReply":       UnsupportedReply,
	}
	want := map[string]string{
		"TokenHeader":            "x-amz-rdma-token",
		"ReplyHeader":            "x-amz-rdma-reply",
		"BytesTransferredHeader": "x-amz-rdma-bytes-transferred",
		"UnsupportedReply":       "501",
	}

	for name, got := range constants {
		if got != want[name] {
			t.Errorf("%s = %q, want %q", name, got, want[name])
		}
	}
}

func TestPutHeaders(t *testing.T) {
	headers := PutHeaders("token-abc")

	if got := headers[TokenHeader]; got != "token-abc" {
		t.Errorf("%s = %q, want %q", TokenHeader, got, "token-abc")
	}
	// An RDMA request carries no HTTP body; a non-zero length would make the
	// server wait for bytes that never arrive.
	if got := headers["Content-Length"]; got != "0" {
		t.Errorf("Content-Length = %q, want %q", got, "0")
	}
}

func TestGetHeaders(t *testing.T) {
	headers := GetHeaders("token-xyz")

	if got := headers[TokenHeader]; got != "token-xyz" {
		t.Errorf("%s = %q, want %q", TokenHeader, got, "token-xyz")
	}
	// A GET must not zero Content-Length: it is a request header for the
	// response body, not the (absent) request body.
	if _, ok := headers["Content-Length"]; ok {
		t.Error("GetHeaders set Content-Length, want it absent")
	}
}

func TestContentMD5(t *testing.T) {
	if got, want := ContentMD5([]byte("hello")), "XUFAKrxLKna5cZ2REBfFkg=="; got != want {
		t.Errorf("ContentMD5() = %q, want %q", got, want)
	}
}

func TestShouldFallbackHeader(t *testing.T) {
	headers := http.Header{}
	if ShouldFallbackHeader(headers) {
		t.Fatal("ShouldFallbackHeader() = true without reply header, want false")
	}

	headers.Set(ReplyHeader, UnsupportedReply)
	if !ShouldFallbackHeader(headers) {
		t.Fatal("ShouldFallbackHeader() = false for unsupported reply, want true")
	}
}

func TestBytesTransferred(t *testing.T) {
	headers := http.Header{}
	if _, ok, err := BytesTransferred(headers); ok || err != nil {
		t.Fatalf("BytesTransferred() = ok %v, err %v; want missing header", ok, err)
	}

	headers.Set(BytesTransferredHeader, "42")
	got, ok, err := BytesTransferred(headers)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != 42 {
		t.Fatalf("BytesTransferred() = (%d, %v), want (42, true)", got, ok)
	}

	headers.Set(BytesTransferredHeader, "-1")
	if _, _, err := BytesTransferred(headers); err == nil {
		t.Fatal("BytesTransferred() succeeded for negative value, want error")
	}

	headers.Set(BytesTransferredHeader, "not-an-int")
	if _, _, err := BytesTransferred(headers); err == nil {
		t.Fatal("BytesTransferred() succeeded for invalid value, want error")
	}
}

func TestRequiredBytesTransferred(t *testing.T) {
	headers := http.Header{}
	if _, err := RequiredBytesTransferred(headers); err == nil {
		t.Fatal("RequiredBytesTransferred() succeeded without header, want error")
	}

	headers.Set(BytesTransferredHeader, "42")
	got, err := RequiredBytesTransferred(headers)
	if err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Fatalf("RequiredBytesTransferred() = %d, want 42", got)
	}
}
