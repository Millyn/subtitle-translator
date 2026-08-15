package device

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSelectFromNumber(t *testing.T) {
	items := []Info{{Index: 2, Name: "USB Mic"}, {Index: 7, Name: "Headset", Default: true}}
	var output strings.Builder
	got, err := SelectFrom(items, strings.NewReader("2\n"), &output)
	if err != nil || got.Index != 2 {
		t.Fatalf("got %#v, %v", got, err)
	}
	if !strings.Contains(output.String(), "USB Mic") {
		t.Fatal("device was not displayed")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestSelectFromMoreErrors(t *testing.T) {
	items := []Info{{Index: 0, Name: "Mic"}}
	if _, err := SelectFrom(items, strings.NewReader("0"), nil); err == nil {
		t.Fatal("nil output")
	}
	if _, err := SelectFrom(items, failingReader{}, &strings.Builder{}); err == nil {
		t.Fatal("scanner error")
	}
	if _, err := SelectFrom(items, strings.NewReader("nope\n"), &strings.Builder{}); err == nil {
		t.Fatal("non-number")
	}
	if _, err := SelectFrom(items, strings.NewReader(""), &strings.Builder{}); !errors.Is(err, ErrCancelled) {
		t.Fatalf("EOF: %v", err)
	}
	if _, err := SelectFrom(items, strings.NewReader("\n"), &strings.Builder{}); err == nil {
		t.Fatal("empty without default")
	}
	if _, err := List(nil); err == nil {
		t.Fatal("nil audio context")
	}
}

func TestSelectFromDefaultAndCancel(t *testing.T) {
	items := []Info{{Index: 4, Name: "Default", Default: true}}
	got, err := SelectFrom(items, strings.NewReader("\n"), &strings.Builder{})
	if err != nil || got.Index != 4 {
		t.Fatalf("default: %#v, %v", got, err)
	}
	_, err = SelectFrom(items, strings.NewReader("q\n"), &strings.Builder{})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestSelectFromErrors(t *testing.T) {
	if _, err := SelectFrom(nil, strings.NewReader("0\n"), &strings.Builder{}); !errors.Is(err, ErrNoDevices) {
		t.Fatalf("empty error = %v", err)
	}
	items := []Info{{Index: 0, Name: "Mic"}}
	if _, err := SelectFrom(items, strings.NewReader("9\n"), &strings.Builder{}); err == nil {
		t.Fatal("expected range error")
	}
}
