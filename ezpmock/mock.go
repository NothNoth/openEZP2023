// Package ezpmock simulates the EZP2023+ firmware as reverse-engineered
// from the vendor software. It is used by the tests and by the demo mode
// of the web UI. It models the host-visible protocol only; it says nothing
// about the real firmware beyond what the host software implies.
package ezpmock

import (
	"encoding/binary"
	"errors"
	"sync"

	"ezp2023/ezp"
)

// Device is an in-memory EZP2023+ with one chip inserted.
type Device struct {
	mu sync.Mutex

	Rev   ezp.Revision
	Bus   byte   // what Detect reports (ezp.BusSPI, ...)
	JEDEC uint32 // reported for ezp.BusSPI
	Mem   []byte // chip contents

	// Log records every command packet, for assertions in tests.
	Log [][]byte

	// ErasePolls is how many status polls report busy after a chip erase.
	ErasePolls int
	// NoSignature makes the 0x09 reply omit the firmware signature.
	NoSignature bool

	page      int
	proto     ezp.Protocol
	busyPolls int
	reply     []byte // pending 64-byte reply
	streamPos int
	streaming bool
	fourByte  bool
	closed    bool
}

// New returns a device holding an erased chip of the given size.
func New(rev ezp.Revision, bus byte, jedec uint32, size int) *Device {
	mem := make([]byte, size)
	for i := range mem {
		mem[i] = 0xFF
	}
	return &Device{Rev: rev, Bus: bus, JEDEC: jedec, Mem: mem, ErasePolls: 3}
}

// ForChip returns a device with an erased chip matching c.
func ForChip(rev ezp.Revision, c *ezp.Chip) *Device {
	bus := map[ezp.Protocol]byte{
		ezp.ProtoSPIFlash:  ezp.BusSPI,
		ezp.ProtoI2C24:     ezp.BusI2C,
		ezp.ProtoMicrowire: ezp.BusMicrowire,
	}[c.Protocol]
	if c.Protocol >= ezp.ProtoSPI25 {
		bus = ezp.BusSPI
	}
	return New(rev, bus, c.JEDEC, int(c.Size))
}

func (d *Device) order() binary.ByteOrder {
	if d.Rev == ezp.RevB {
		return binary.LittleEndian
	}
	return binary.BigEndian
}

// FourByteMode reports whether a B7 pass-through was received.
func (d *Device) FourByteMode() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.fourByte }

func (d *Device) Revision() ezp.Revision { return d.Rev }

var errClosed = errors.New("ezpmock: device closed")

func (d *Device) WriteCommand(p []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errClosed
	}
	if len(p) != ezp.PacketSize {
		return errors.New("ezpmock: command must be 64 bytes")
	}
	d.Log = append(d.Log, append([]byte(nil), p...))
	d.reply, d.streaming = nil, false
	r := make([]byte, ezp.PacketSize)
	o := d.order()
	switch p[1] {
	case ezp.OpConfigure:
		d.page = int(o.Uint16(p[4:]))
		d.proto = ezp.Protocol(p[2])
		d.reply = r
	case ezp.OpDetect:
		if d.Bus != 0 {
			r[0] = d.Bus
			r[1], r[2], r[3] = byte(d.JEDEC>>16), byte(d.JEDEC>>8), byte(d.JEDEC)
		}
		if !d.NoSignature {
			binary.BigEndian.PutUint32(r[60:], ezp.DetectSignature)
		}
		d.reply = r
	case ezp.OpStream:
		d.streamPos = int(o.Uint32(p[8:]))
		d.streaming = true
		d.reply = r
	case ezp.OpErase:
		arg := int(o.Uint16(p[0x1A:]))
		switch {
		case d.proto == ezp.ProtoSPIFlash && arg == ezp.EraseChipArg:
			d.fill(0, len(d.Mem))
			d.busyPolls = d.ErasePolls
		case d.proto == ezp.ProtoMicrowire:
			d.fill(0, len(d.Mem)) // single "erase all"
		default:
			d.fill(arg, arg+max(d.page, 1))
		}
	case ezp.OpStatus:
		if d.busyPolls > 0 {
			d.busyPolls--
			r[0] = 1
		}
		d.reply = r
	case ezp.OpSPI:
		if p[3] >= 1 && p[4] == 0xB7 {
			d.fourByte = true
		}
		d.reply = r
	case ezp.OpEndSession:
		d.fourByte = false
	default:
		return errors.New("ezpmock: unknown opcode")
	}
	return nil
}

func (d *Device) fill(from, to int) {
	for i := max(from, 0); i < to && i < len(d.Mem); i++ {
		d.Mem[i] = 0xFF
	}
}

func (d *Device) WriteData(p []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errClosed
	}
	if !d.streaming {
		return errors.New("ezpmock: data written outside a stream")
	}
	d.reply = nil // the 0x05 reply is not read when writing
	for _, b := range p {
		if d.streamPos < len(d.Mem) {
			d.Mem[d.streamPos] &= b // flash can only clear bits
		}
		d.streamPos++
	}
	return nil
}

func (d *Device) Read(n int) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, errClosed
	}
	if d.reply != nil {
		r := d.reply[:min(n, len(d.reply))]
		d.reply = d.reply[len(r):]
		if len(d.reply) == 0 {
			d.reply = nil
		}
		return r, nil
	}
	if !d.streaming {
		return nil, errors.New("ezpmock: read with nothing pending (would time out)")
	}
	end := min(d.streamPos+n, len(d.Mem))
	if end <= d.streamPos {
		return nil, errors.New("ezpmock: read past end of chip")
	}
	out := append([]byte(nil), d.Mem[d.streamPos:end]...)
	d.streamPos = end
	return out, nil
}

func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	return nil
}
