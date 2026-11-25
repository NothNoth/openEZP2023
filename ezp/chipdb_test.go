package ezp_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"ezp2023/ezp"
)

// chips.json must be exactly what MarshalChipsJSON produces, so that a
// regenerated file (go run ./cmd/ezp-dat2json) gives a clean diff.
func TestChipsJSONCanonical(t *testing.T) {
	want, err := os.ReadFile("chips.json")
	if err != nil {
		t.Fatal(err)
	}
	chips, err := ezp.ParseChipsJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ezp.MarshalChipsJSON(chips)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("chips.json is not in canonical form; regenerate it with MarshalChipsJSON")
	}
}

func TestChipsJSONErrors(t *testing.T) {
	for _, in := range []string{
		`[{"index":0,"protocol":"usb"}]`,
		`[{"index":0,"protocol":"24xx","voltage":"12V"}]`,
		`[{"index":0,"protocol":"spi-flash","voltage":"3.3V","jedec":"XYZ"}]`,
		`[{"index":0,"protocol":"24xx","voltage":"5V"},{"index":0,"protocol":"24xx","voltage":"5V"}]`,
	} {
		if _, err := ezp.ParseChipsJSON([]byte(in)); err == nil {
			t.Errorf("accepted %s", in)
		}
	}
}

// TestParseDat checks the vendor .Dat decoder on a synthetic file: one
// record with GBK full-width parentheses, then an empty record.
func TestParseDat(t *testing.T) {
	b := make([]byte, 2*ezp.RecordSize)
	copy(b, "SPI_FLASH,AMIC,A25L16P\xa3\xa8bottom\xa3\xa9")
	le := binary.LittleEndian
	le.PutUint32(b[0x30:], 0x372015)
	le.PutUint32(b[0x34:], 2<<20)
	le.PutUint16(b[0x38:], 256)
	b[0x3A] = byte(ezp.ProtoSPIFlash)
	b[0x3B] = 3
	le.PutUint16(b[0x3C:], 500)
	le.PutUint16(b[0x3E:], 7)
	le.PutUint16(b[0x40:], 8)
	b[0x42] = 9
	b[0x43] = byte(ezp.Volt1V8)

	chips, err := ezp.ParseDat(b)
	if err != nil {
		t.Fatal(err)
	}
	want := ezp.Chip{
		Index: 0, Type: "SPI_FLASH", Vendor: "AMIC", Model: "A25L16P(bottom)",
		JEDEC: 0x372015, Size: 2 << 20, Page: 256, Protocol: ezp.ProtoSPIFlash,
		Sub: 3, Timing: 500, Voltage: ezp.Volt1V8, Extend: 7, EEPROM: 8, EEPROMPage: 9,
	}
	if len(chips) != 1 || chips[0] != want {
		t.Fatalf("got %+v", chips)
	}
	if _, err := ezp.ParseDat(b[:ezp.RecordSize+1]); err == nil {
		t.Fatal("accepted a truncated file")
	}
}
