package vsockexec

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestWriteReadFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, FrameStdout, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	typ, payload, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if typ != FrameStdout || string(payload) != "hello" {
		t.Errorf("got (%d, %q), want (%d, %q)", typ, payload, FrameStdout, "hello")
	}
}

func TestWriteReadFrameEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, FrameStdin, nil); err != nil {
		t.Fatal(err)
	}
	typ, payload, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if typ != FrameStdin || len(payload) != 0 {
		t.Errorf("got (%d, %v), want (%d, empty)", typ, payload, FrameStdin)
	}
}

func TestReadFrameRejectsOversizedLength(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(FrameStdout)
	var lenBytes [4]byte
	binary.BigEndian.PutUint32(lenBytes[:], maxFrameLen+1)
	buf.Write(lenBytes[:])

	if _, _, err := ReadFrame(&buf); err == nil {
		t.Error("ReadFrame with oversized length = nil error, want error")
	}
}

func TestWriteReadHeaderRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := Header{Argv: []string{"sh", "-c", "echo hi"}, TTY: true, Cols: 80, Rows: 24}
	if err := WriteHeader(&buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadHeader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadHeader() = %+v, want %+v", got, want)
	}
}

func TestReadHeaderRejectsWrongFrameType(t *testing.T) {
	var buf bytes.Buffer
	WriteFrame(&buf, FrameStdout, []byte("not a header"))
	if _, err := ReadHeader(&buf); err == nil {
		t.Error("ReadHeader() on a non-header frame = nil error, want error")
	}
}

func TestWriteReadResizeRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteResize(&buf, 24, 80); err != nil {
		t.Fatal(err)
	}
	typ, payload, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if typ != FrameResize {
		t.Fatalf("frame type = %d, want %d", typ, FrameResize)
	}
	rows, cols, err := ReadResize(payload)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 24 || cols != 80 {
		t.Errorf("ReadResize() = (%d, %d), want (24, 80)", rows, cols)
	}
}

func TestWriteReadExitRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteExit(&buf, 137); err != nil {
		t.Fatal(err)
	}
	typ, payload, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if typ != FrameExit {
		t.Fatalf("frame type = %d, want %d", typ, FrameExit)
	}
	code, err := ReadExit(payload)
	if err != nil {
		t.Fatal(err)
	}
	if code != 137 {
		t.Errorf("ReadExit() = %d, want 137", code)
	}
}
