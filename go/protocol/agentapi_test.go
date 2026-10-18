package protocol

import (
	"encoding/hex"
	"testing"
)

func TestDiagnosticAPIReadVector(t *testing.T) {
	// CBOR map(1), text(4) path, text(19) /api/energy/history.
	body, err := hex.DecodeString("a16470617468732f6170692f656e657267792f686973746f7279")
	if err != nil {
		t.Fatal(err)
	}
	read, err := DecodeAPIRead(body)
	if err != nil || read.Path != "/api/energy/history" || read.MaxBytes != 0 {
		t.Fatalf("read vector: %+v %v", read, err)
	}
	// A method field cannot add a write path to this read.
	bad, _ := hex.DecodeString("a26470617468732f6170692f656e657267792f686973746f7279666d6574686f6464504f5354")
	if _, err := DecodeAPIRead(bad); err == nil {
		t.Fatal("unknown method field accepted")
	}
}
