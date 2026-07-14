package lsp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestWriteFrameReadFrameRoundTrip(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)

	var buf bytes.Buffer
	if err := writeFrame(&buf, payload); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}

	got, err := readFrame(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("readFrame = %q, want %q", got, payload)
	}
}

func TestReadFrameTwoMessagesBackToBack(t *testing.T) {
	first := []byte(`{"jsonrpc":"2.0","id":1,"method":"a"}`)
	second := []byte(`{"jsonrpc":"2.0","id":2,"method":"b"}`)

	var buf bytes.Buffer
	if err := writeFrame(&buf, first); err != nil {
		t.Fatalf("writeFrame(first): %v", err)
	}
	if err := writeFrame(&buf, second); err != nil {
		t.Fatalf("writeFrame(second): %v", err)
	}

	r := bufio.NewReader(&buf)
	got1, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame(1st): %v", err)
	}
	if !bytes.Equal(got1, first) {
		t.Errorf("1st frame = %q, want %q", got1, first)
	}
	got2, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame(2nd): %v", err)
	}
	if !bytes.Equal(got2, second) {
		t.Errorf("2nd frame = %q, want %q", got2, second)
	}
}

func TestReadFrameEOFAtStreamEnd(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(""))
	_, err := readFrame(r)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("readFrame on empty stream = %v, want io.EOF", err)
	}
}

func TestReadFrameErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"missing content-length", "X-Other: 1\r\n\r\n{}"},
		{"garbled content-length", "Content-Length: notanumber\r\n\r\n{}"},
		{"negative content-length", "Content-Length: -1\r\n\r\n{}"},
		{"truncated body", "Content-Length: 100\r\n\r\n{\"short\":true}"},
		{"malformed header line", "not-a-header-line\r\n\r\n{}"},
		{"eof mid header", "Content-Length: 5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readFrame(bufio.NewReader(strings.NewReader(tt.raw)))
			if err == nil {
				t.Fatal("readFrame err = nil, want an error")
			}
			if errors.Is(err, io.EOF) {
				t.Fatal("readFrame err = io.EOF, want a wrapped error (frame was non-empty/truncated, not a clean stream end)")
			}
		})
	}
}

func TestReadFrameHeaderCaseInsensitiveAndExtraHeaders(t *testing.T) {
	payload := []byte(`{"ok":true}`)
	raw := fmt.Sprintf("content-TYPE: application/vscode-jsonrpc; charset=utf-8\r\ncontent-length: %d\r\n\r\n%s",
		len(payload), payload)

	got, err := readFrame(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("readFrame = %q, want %q", got, payload)
	}
}
