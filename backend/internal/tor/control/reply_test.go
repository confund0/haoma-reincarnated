package control

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func newReader(s string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(s))
}

func TestReadOne_SingleLineReply(t *testing.T) {
	reply, event, err := readOne(newReader("250 OK\r\n"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if event != nil {
		t.Fatalf("unexpected event: %+v", event)
	}
	if reply == nil || reply.Code != 250 || len(reply.Lines) != 1 || reply.Lines[0] != "OK" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestReadOne_MultiLineReply(t *testing.T) {
	reply, _, err := readOne(newReader("250-key=value\r\n250-other=thing\r\n250 OK\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if reply.Code != 250 || len(reply.Lines) != 3 {
		t.Errorf("reply = %+v", reply)
	}
	want := []string{"key=value", "other=thing", "OK"}
	for i, w := range want {
		if reply.Lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, reply.Lines[i], w)
		}
	}
}

func TestReadOne_DataPayloadReply(t *testing.T) {
	input := "250+info/names=\r\nfirst\r\nsecond\r\n.\r\n250 OK\r\n"
	reply, _, err := readOne(newReader(input))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"info/names=", "first", "second", "OK"}
	if len(reply.Lines) != len(want) {
		t.Fatalf("lines = %v, want %v", reply.Lines, want)
	}
	for i := range want {
		if reply.Lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, reply.Lines[i], want[i])
		}
	}
}

func TestReadOne_Event_Single(t *testing.T) {
	reply, event, err := readOne(newReader("650 CIRC 123 BUILT $abc\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if reply != nil {
		t.Errorf("unexpected reply: %+v", reply)
	}
	if event == nil || event.Type != "CIRC" {
		t.Errorf("event = %+v", event)
	}
}

func TestReadOne_Event_NoArgs(t *testing.T) {
	_, event, err := readOne(newReader("650 SIGNAL\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if event == nil || event.Type != "SIGNAL" {
		t.Errorf("event = %+v", event)
	}
}

func TestReadOne_ErrorCode(t *testing.T) {
	reply, _, err := readOne(newReader("551 Service descriptor is not well-formed\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if reply.Code != 551 {
		t.Errorf("code = %d", reply.Code)
	}
}

func TestReadOne_BadSeparator(t *testing.T) {
	_, _, err := readOne(newReader("250@OK\r\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReadOne_ShortLine(t *testing.T) {
	_, _, err := readOne(newReader("25\r\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReadOne_NonNumericCode(t *testing.T) {
	_, _, err := readOne(newReader("ABC OK\r\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReadOne_EOF(t *testing.T) {
	_, _, err := readOne(newReader(""))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
}
