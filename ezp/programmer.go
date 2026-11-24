package ezp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNoChip      = errors.New("ezp: no chip detected; check the chip seating and orientation")
	ErrEraseTimout = errors.New("ezp: erase did not finish before the chip's timeout")
	ErrTooLarge    = errors.New("ezp: data is larger than the chip")
)

// ProgressFunc is called with the bytes (or steps) done so far and the total.
type ProgressFunc func(done, total int)

// Bus values returned by Detect.
const (
	BusNone      = 0
	BusSPI       = 1
	BusI2C       = 2
	BusMicrowire = 3
)

// DetectSignature is the constant the firmware puts in bytes 60..63 of the
// 0x09 reply (big-endian), as observed on real hardware by libezp2023plus.
// The vendor software does not check it.
const DetectSignature = 0x9A7336BD

// DetectResult is the decoded reply of the 0x09 command.
type DetectResult struct {
	Bus   byte   // BusNone, BusSPI, BusI2C or BusMicrowire
	JEDEC uint32 // only meaningful when Bus == BusSPI
	// Signature is bytes 60..63 of the reply. SignatureOK is false when it
	// differs from DetectSignature: the reply may be garbled, out of step,
	// or the hardware revision may have been misidentified.
	Signature   uint32
	SignatureOK bool
	Raw         []byte // the full 64-byte reply
}

// Programmer drives an EZP2023+ over a Transport. It is not safe for
// concurrent use.
type Programmer struct {
	T     Transport
	Clock SPIClock

	// ReplyDelay is an optional wait between a command and reading its
	// reply. The vendor software waits 50 ms, but the read blocks until the
	// reply arrives anyway (libezp2023plus reads immediately on real
	// hardware), so the default is 0.
	ReplyDelay time.Duration
	// PollInterval is the wait between two status polls while erasing.
	// The vendor loop takes 100 ms per poll (50 ms sleep + its 50 ms reply
	// delay), and the chip's Timing field counts polls, so this keeps the
	// vendor's erase timeout: Timing × 100 ms.
	PollInterval time.Duration
	// ReadChunk overrides the bulk read size. 0 = vendor behaviour
	// (page size, minimum 64). Larger values are much faster over WebUSB
	// but untested on real hardware.
	ReadChunk int
	// Progress, if set, is called during long operations.
	Progress ProgressFunc
}

// New returns a Programmer with the default timings.
func New(t Transport) *Programmer {
	return &Programmer{T: t, PollInterval: 100 * time.Millisecond}
}

func (p *Programmer) rev() Revision { return p.T.Revision() }

func (p *Programmer) progress(done, total int) {
	if p.Progress != nil {
		p.Progress(done, total)
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Command sends a packet and, if reply is true, waits ReplyDelay and reads
// the 64-byte reply.
func (p *Programmer) Command(ctx context.Context, pkt *Packet, reply bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.T.WriteCommand(pkt[:]); err != nil {
		return nil, fmt.Errorf("ezp: command 0x%02X: %w", pkt[1], err)
	}
	if !reply {
		return nil, nil
	}
	if err := sleep(ctx, p.ReplyDelay); err != nil {
		return nil, err
	}
	r, err := p.readFull(PacketSize)
	if err != nil {
		return nil, fmt.Errorf("ezp: reply to 0x%02X: %w", pkt[1], err)
	}
	return r, nil
}

func (p *Programmer) readFull(n int) ([]byte, error) {
	buf := make([]byte, 0, n)
	for len(buf) < n {
		b, err := p.T.Read(n - len(buf))
		if err != nil {
			return buf, err
		}
		if len(b) == 0 {
			return buf, errors.New("empty read")
		}
		buf = append(buf, b...)
	}
	return buf, nil
}

func (p *Programmer) endSession(ctx context.Context) error {
	// Sent even if ctx is cancelled, so the firmware returns to idle.
	_, err := p.Command(context.WithoutCancel(ctx), EndSessionPacket(), false)
	return err
}

// withSession runs fn between an OpConfigure and an OpEndSession.
func (p *Programmer) withSession(ctx context.Context, c *Chip, fn func() error) (err error) {
	if _, err := p.Command(ctx, ConfigurePacket(p.rev(), c, p.Clock), true); err != nil {
		return err
	}
	defer func() {
		if e := p.endSession(ctx); err == nil {
			err = e
		}
	}()
	return fn()
}

// defaultDetectChip is used when Detect is called without a chip.
var defaultDetectChip = Chip{Protocol: ProtoSPIFlash, Page: 256}

// Detect asks the programmer which bus responds and, for SPI flash, the
// JEDEC ID. c selects the parameters sent along; nil means generic SPI.
func (p *Programmer) Detect(ctx context.Context, c *Chip) (DetectResult, error) {
	if c == nil {
		c = &defaultDetectChip
	}
	r, err := p.Command(ctx, DetectPacket(p.rev(), c, p.Clock), true)
	if err != nil {
		return DetectResult{}, err
	}
	if err := p.endSession(ctx); err != nil {
		return DetectResult{}, err
	}
	sig := uint32(r[60])<<24 | uint32(r[61])<<16 | uint32(r[62])<<8 | uint32(r[63])
	res := DetectResult{Bus: r[0], Raw: r, Signature: sig, SignatureOK: sig == DetectSignature}
	if res.Bus == BusSPI {
		res.JEDEC = uint32(r[1])<<16 | uint32(r[2])<<8 | uint32(r[3])
	}
	return res, nil
}

// checkPresent mirrors the vendor software: for SPI flash, 24xx and 93xx it
// runs a detection first and refuses to continue if nothing answers.
func (p *Programmer) checkPresent(ctx context.Context, c *Chip) error {
	if c.Protocol >= ProtoSPI25 {
		return nil
	}
	r, err := p.Detect(ctx, c)
	if err != nil {
		return err
	}
	if r.Bus == BusNone {
		return ErrNoChip
	}
	return nil
}

func (p *Programmer) enter4ByteIfNeeded(ctx context.Context, c *Chip) error {
	if c.Protocol == ProtoSPIFlash && c.Size > 0x1000000 {
		_, err := p.Command(ctx, SPIPacket([]byte{0xB7}), true)
		return err
	}
	return nil
}

func (p *Programmer) chunk(c *Chip) int {
	n := int(c.Page)
	if n < 64 {
		n = 64
	}
	return n
}

// readStream reads the whole chip inside an already open session.
func (p *Programmer) readStream(ctx context.Context, c *Chip, consume func(off int, b []byte) error) error {
	if err := p.enter4ByteIfNeeded(ctx, c); err != nil {
		return err
	}
	if _, err := p.Command(ctx, StreamPacket(p.rev(), 0), true); err != nil {
		return err
	}
	chunk := p.chunk(c)
	if p.ReadChunk > 0 {
		chunk = p.ReadChunk
	}
	total := int(c.Size)
	for off := 0; off < total; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(chunk, total-off)
		b, err := p.readFull(n)
		if err != nil {
			return fmt.Errorf("ezp: read at 0x%X: %w", off, err)
		}
		if err := consume(off, b); err != nil {
			return err
		}
		off += n
		p.progress(off, total)
	}
	return nil
}

// Read dumps the whole chip.
func (p *Programmer) Read(ctx context.Context, c *Chip) ([]byte, error) {
	if err := p.checkPresent(ctx, c); err != nil {
		return nil, err
	}
	out := make([]byte, 0, c.Size)
	err := p.withSession(ctx, c, func() error {
		return p.readStream(ctx, c, func(_ int, b []byte) error {
			out = append(out, b...)
			return nil
		})
	})
	return out, err
}

// Verify compares the chip with data. It returns -1 if they match, or the
// offset of the first difference. Bytes past len(data) are not checked.
func (p *Programmer) Verify(ctx context.Context, c *Chip, data []byte) (int, error) {
	if len(data) > int(c.Size) {
		return 0, ErrTooLarge
	}
	return p.compare(ctx, c, len(data), func(off int, b []byte) int {
		return firstDiff(b, data[off:off+len(b)])
	})
}

// BlankCheck returns -1 if the chip is fully erased (all 0xFF), or the
// offset of the first programmed byte.
func (p *Programmer) BlankCheck(ctx context.Context, c *Chip) (int, error) {
	return p.compare(ctx, c, int(c.Size), func(_ int, b []byte) int {
		for i, v := range b {
			if v != 0xFF {
				return i
			}
		}
		return -1
	})
}

// compare streams the chip and calls check on the first limit bytes;
// check returns the index of a mismatch within b, or -1.
func (p *Programmer) compare(ctx context.Context, c *Chip, limit int, check func(off int, b []byte) int) (int, error) {
	if err := p.checkPresent(ctx, c); err != nil {
		return 0, err
	}
	mismatch := -1
	errStop := errors.New("stop")
	err := p.withSession(ctx, c, func() error {
		return p.readStream(ctx, c, func(off int, b []byte) error {
			if off >= limit {
				return nil
			}
			if i := check(off, b[:min(len(b), limit-off)]); i >= 0 {
				mismatch = off + i
				return errStop
			}
			return nil
		})
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return mismatch, err
}

func firstDiff(a, b []byte) int {
	for i := range b {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

// TrimmedLength is the length the vendor software actually programs:
// trailing 0xFF bytes are dropped (an erased chip already holds them) and
// the result is rounded up to a whole page.
func TrimmedLength(data []byte, page int) int {
	n := len(data)
	for n > 0 && data[n-1] == 0xFF {
		n--
	}
	if page > 0 && n%page != 0 {
		n = (n/page + 1) * page
	}
	return n
}

// Write programs data from address 0. The chip must be erased first (see
// Erase). Like the vendor software, trailing 0xFF bytes are skipped.
func (p *Programmer) Write(ctx context.Context, c *Chip, data []byte) error {
	if len(data) > int(c.Size) {
		return ErrTooLarge
	}
	if err := p.checkPresent(ctx, c); err != nil {
		return err
	}
	chunk := p.chunk(c)
	n := TrimmedLength(data, int(c.Page))
	if n > int(c.Size) {
		n = int(c.Size)
	}
	// Pad to the programmed length with 0xFF.
	buf := make([]byte, n)
	copy(buf, data)
	for i := len(data); i < n; i++ {
		buf[i] = 0xFF
	}
	return p.withSession(ctx, c, func() error {
		if err := p.enter4ByteIfNeeded(ctx, c); err != nil {
			return err
		}
		if _, err := p.Command(ctx, StreamPacket(p.rev(), 0), false); err != nil {
			return err
		}
		for off := 0; off < n; off += chunk {
			if err := ctx.Err(); err != nil {
				return err
			}
			end := min(off+chunk, n)
			block := buf[off:end]
			if len(block) < chunk { // keep every transfer a full chunk
				block = append(append([]byte{}, block...), bytes.Repeat([]byte{0xFF}, chunk-len(block))...)
			}
			if err := p.T.WriteData(block); err != nil {
				return fmt.Errorf("ezp: write at 0x%X: %w", off, err)
			}
			p.progress(end, n)
		}
		if c.Protocol == ProtoI2C24 {
			return sleep(ctx, 100*time.Millisecond)
		}
		return nil
	})
}

// Erase erases the chip. SPI flash uses a chip erase and polls the busy
// bit; EEPROMs are erased page by page (or in one command for 93xx).
func (p *Programmer) Erase(ctx context.Context, c *Chip) error {
	if err := p.checkPresent(ctx, c); err != nil {
		return err
	}
	rev := p.rev()
	return p.withSession(ctx, c, func() error {
		switch c.Protocol {
		case ProtoSPIFlash:
			if _, err := p.Command(ctx, ErasePacket(rev, EraseChipArg), false); err != nil {
				return err
			}
			limit := int(c.Timing)
			for i := 0; ; i++ {
				if err := sleep(ctx, p.PollInterval); err != nil {
					return err
				}
				r, err := p.Command(ctx, StatusPacket(), true)
				if err != nil {
					return err
				}
				if r[0]&1 == 0 {
					p.progress(limit, limit)
					return nil
				}
				if i >= limit {
					return ErrEraseTimout
				}
				p.progress(i, limit)
			}
		case ProtoMicrowire:
			_, err := p.Command(ctx, ErasePacket(rev, c.Timing), false)
			return err
		default: // 24xx, 25xx, 95xx: one command per page
			page := int(c.Page)
			if page == 0 {
				page = 1
			}
			for addr := 0; addr < int(c.Size); addr += page {
				// The vendor software sends a u16; kept as-is.
				if _, err := p.Command(ctx, ErasePacket(rev, uint16(addr)), false); err != nil {
					return err
				}
				p.progress(addr+page, int(c.Size))
			}
			return nil
		}
	})
}

// SPI sends a raw SPI pass-through command inside its own session and
// returns the 64-byte reply. Experimental: only 0x9F and 0xB7 are used by
// the vendor software, and the reply format is not documented.
func (p *Programmer) SPI(ctx context.Context, c *Chip, tx []byte) ([]byte, error) {
	if len(tx) > PacketSize-4 {
		return nil, errors.New("ezp: SPI transfer too long")
	}
	var r []byte
	err := p.withSession(ctx, c, func() (err error) {
		r, err = p.Command(ctx, SPIPacket(tx), true)
		return err
	})
	return r, err
}
