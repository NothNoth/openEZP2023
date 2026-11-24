package ezp_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math/rand"
	"os"
	"testing"

	"ezp2023/ezp"
	"ezp2023/ezpmock"
)

func loadChips(t *testing.T) []ezp.Chip {
	t.Helper()
	b, err := os.ReadFile("testdata/chips.dat")
	if err != nil {
		t.Fatal(err)
	}
	chips, err := ezp.ParseDat(b)
	if err != nil {
		t.Fatal(err)
	}
	return chips
}

func chip(t *testing.T, chips []ezp.Chip, model string) *ezp.Chip {
	t.Helper()
	for i := range chips {
		if chips[i].Model == model {
			return &chips[i]
		}
	}
	t.Fatalf("chip %q not found", model)
	return nil
}

func fast(t ezp.Transport) *ezp.Programmer {
	p := ezp.New(t)
	p.ReplyDelay = 0
	return p
}

func TestParseDat(t *testing.T) {
	chips := loadChips(t)
	if len(chips) != 948 {
		t.Fatalf("got %d chips", len(chips))
	}
	w := chip(t, chips, "W25Q128")
	if w.JEDEC != 0xEF4018 || w.Size != 16<<20 || w.Page != 256 || w.Protocol != ezp.ProtoSPIFlash {
		t.Fatalf("W25Q128 decoded wrong: %+v", w)
	}
	chip(t, chips, "A25L16P(bottom)") // GBK full-width parenthesis
	if got := ezp.FindByJEDEC(chips, 0xEF4017); len(got) == 0 {
		t.Fatal("W25Q64 not found by JEDEC")
	}
	at := chip(t, chips, "AT24C02")
	if at.Protocol != ezp.ProtoI2C24 || at.Size != 256 || at.Page != 8 {
		t.Fatalf("AT24C02 decoded wrong: %+v", at)
	}
}

func TestVoltage(t *testing.T) {
	chips := loadChips(t)
	count := map[ezp.Voltage]int{}
	for _, c := range chips {
		count[c.Voltage]++
	}
	if count[ezp.Volt3V3] == 0 || count[ezp.Volt5V] == 0 {
		t.Fatalf("voltage distribution looks wrong: %v", count)
	}
	cases := map[string]bool{
		"W25Q64FW(1.8V)": true,  // tagged in the name
		"MX25U3235F":     true,  // known family, not tagged
		"SST25WF040":     true,  // known family, not tagged
		"GD25LQ40":       true,  // known family, not tagged
		"EN25LF40":       true,  // voltage byte says 1.8 V
		"W25Q64":         false, // 3.3 V
		"MX25L12835F":    false, // 3.3 V
		"AT24C02":        false,
	}
	for model, want := range cases {
		if got := chip(t, chips, model).Needs18VAdapter(); got != want {
			t.Errorf("%s: Needs18VAdapter = %v, want %v", model, got, want)
		}
	}
	// Family rules on names that carry no "(1.8V)" tag.
	families := map[string]bool{
		"W25Q64JW": true, "W25Q128FW": true, "W25Q16DW": true, "W25Q80BW": true,
		"W25Q64JV": false, "W25Q128FV": false, "W25Q80DV": false, "W25Q64": false,
		"BY25Q32AL": true, "BY25Q32BS": false, "BY25Q16": false,
		"MX25U6435F": true, "MX25L6406E": false, "GD25Q64": false,
	}
	for model, want := range families {
		if got := (&ezp.Chip{Model: model}).Needs18VAdapter(); got != want {
			t.Errorf("family rule %s: got %v, want %v", model, got, want)
		}
	}
	c := &ezp.Chip{Protocol: ezp.ProtoI2C24, Voltage: ezp.Volt5V}
	if p := ezp.ConfigurePacket(ezp.RevA, c, 0); p[0x1C] != 2 || p[0x10] != 2 {
		t.Errorf("voltage not sent: [0x10]=%d [0x1C]=%d", p[0x10], p[0x1C])
	}
	c = &ezp.Chip{Protocol: ezp.ProtoSPI25, Voltage: ezp.Volt5V}
	if p := ezp.ConfigurePacket(ezp.RevA, c, ezp.Clock3MHz); p[0x10] != 2<<4+2 {
		t.Errorf("25xx clock/voltage byte = %#x", p[0x10])
	}
}

func TestPacketByteOrder(t *testing.T) {
	c := &ezp.Chip{Protocol: ezp.ProtoSPIFlash, Size: 0x01000000, Page: 0x0100, Timing: 1000, JEDEC: 0xEF4018}
	a := ezp.ConfigurePacket(ezp.RevA, c, ezp.Clock6MHz)
	b := ezp.ConfigurePacket(ezp.RevB, c, ezp.Clock6MHz)
	if got := binary.BigEndian.Uint32(a[8:]); got != c.Size {
		t.Errorf("RevA size not big-endian: % x", a[8:12])
	}
	if got := binary.LittleEndian.Uint32(b[8:]); got != c.Size {
		t.Errorf("RevB size not little-endian: % x", b[8:12])
	}
	if a[1] != ezp.OpConfigure || a[2] != 0 || a[0x10] != byte(ezp.Clock6MHz) {
		t.Errorf("configure header wrong: % x", a[:0x20])
	}
	e := ezp.ErasePacket(ezp.RevA, ezp.EraseChipArg)
	if e[0] != 1 || e[1] != ezp.OpErase || e[0x1A] != 0x80 || e[0x1B] != 0 {
		t.Errorf("erase packet wrong: % x", e[:0x20])
	}
}

func TestDetect(t *testing.T) {
	for _, rev := range []ezp.Revision{ezp.RevA, ezp.RevB} {
		d := ezpmock.New(rev, ezp.BusSPI, 0xEF4018, 1024)
		r, err := fast(d).Detect(context.Background(), nil)
		if err != nil || r.Bus != ezp.BusSPI || r.JEDEC != 0xEF4018 {
			t.Fatalf("rev %v: %+v %v", rev, r, err)
		}
		if last := d.Log[len(d.Log)-1]; last[1] != ezp.OpEndSession {
			t.Fatal("detect did not end the session")
		}
	}
}

func TestNoChip(t *testing.T) {
	chips := loadChips(t)
	d := ezpmock.New(ezp.RevA, ezp.BusNone, 0, 1<<16)
	_, err := fast(d).Read(context.Background(), chip(t, chips, "W25Q80"))
	if !errors.Is(err, ezp.ErrNoChip) {
		t.Fatalf("want ErrNoChip, got %v", err)
	}
}

func roundTrip(t *testing.T, rev ezp.Revision, c *ezp.Chip) *ezpmock.Device {
	t.Helper()
	ctx := context.Background()
	d := ezpmock.ForChip(rev, c)
	rand.New(rand.NewSource(1)).Read(d.Mem) // dirty chip
	p := fast(d)

	if err := p.Erase(ctx, c); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if off, err := p.BlankCheck(ctx, c); err != nil || off != -1 {
		t.Fatalf("blank check after erase: off=%d err=%v", off, err)
	}
	data := make([]byte, c.Size-uint32(c.Page)/2) // not page aligned
	rand.New(rand.NewSource(2)).Read(data)
	if err := p.Write(ctx, c, data); err != nil {
		t.Fatalf("write: %v", err)
	}
	if off, err := p.Verify(ctx, c, data); err != nil || off != -1 {
		t.Fatalf("verify: off=%d err=%v", off, err)
	}
	got, err := p.Read(ctx, c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got[:len(data)], data) || len(got) != int(c.Size) {
		t.Fatal("read back differs")
	}
	return d
}

func TestRoundTripSPIFlash(t *testing.T) {
	chips := loadChips(t)
	for _, rev := range []ezp.Revision{ezp.RevA, ezp.RevB} {
		roundTrip(t, rev, chip(t, chips, "W25Q80"))
	}
}

func TestRoundTripEEPROMs(t *testing.T) {
	chips := loadChips(t)
	for _, m := range []string{"AT24C02", "AT25640", "AT93C46@16", "M95040"} {
		t.Run(m, func(t *testing.T) { roundTrip(t, ezp.RevB, chip(t, chips, m)) })
	}
}

func TestWriteWithoutEraseIsCaughtByVerify(t *testing.T) {
	chips := loadChips(t)
	c := chip(t, chips, "W25Q80")
	d := ezpmock.ForChip(ezp.RevA, c)
	for i := range d.Mem {
		d.Mem[i] = 0x00
	}
	p := fast(d)
	data := bytes.Repeat([]byte{0x5A}, 4096)
	if err := p.Write(context.Background(), c, data); err != nil {
		t.Fatal(err)
	}
	off, err := p.Verify(context.Background(), c, data)
	if err != nil || off != 0 {
		t.Fatalf("want mismatch at 0, got %d %v", off, err)
	}
}

func TestFourByteMode(t *testing.T) {
	c := &ezp.Chip{Protocol: ezp.ProtoSPIFlash, Size: 32 << 20, Page: 256, Timing: 1000, JEDEC: 0xC84019}
	d := ezpmock.ForChip(ezp.RevA, c)
	p := fast(d)
	p.ReadChunk = 1 << 20
	sawB7 := false
	p.Progress = func(done, total int) {
		if done == 1<<20 {
			sawB7 = d.FourByteMode()
		}
	}
	if _, err := p.Read(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !sawB7 {
		t.Fatal("4-byte mode not entered for a 32 MB chip")
	}
}

func TestCancelEndsSession(t *testing.T) {
	chips := loadChips(t)
	c := chip(t, chips, "W25Q80")
	d := ezpmock.ForChip(ezp.RevA, c)
	p := fast(d)
	ctx, cancel := context.WithCancel(context.Background())
	p.Progress = func(done, _ int) {
		if done >= 4096 {
			cancel()
		}
	}
	_, err := p.Read(ctx, c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if last := d.Log[len(d.Log)-1]; last[1] != ezp.OpEndSession {
		t.Fatal("cancelled read did not end the session")
	}
}

func TestEraseTimeout(t *testing.T) {
	c := &ezp.Chip{Protocol: ezp.ProtoSPIFlash, Size: 4096, Page: 256, Timing: 2}
	d := ezpmock.ForChip(ezp.RevA, c)
	d.ErasePolls = 10
	err := fast(d).Erase(context.Background(), c)
	if !errors.Is(err, ezp.ErrEraseTimout) {
		t.Fatalf("want ErrEraseTimout, got %v", err)
	}
}
