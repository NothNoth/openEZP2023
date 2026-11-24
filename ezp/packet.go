package ezp

import "encoding/binary"

// USB identifiers of the EZP2023+.
const (
	VendorID     = 0x1FC8
	ProductID    = 0x310B
	Manufacturer = "www.zhifengsoft.com"
)

// PacketSize is the size of every command and reply packet.
const PacketSize = 64

// Opcodes (packet byte 1).
const (
	OpErase      = 0x02
	OpStream     = 0x05
	OpConfigure  = 0x07
	OpEndSession = 0x08
	OpDetect     = 0x09
	OpStatus     = 0x0A
	OpSPI        = 0x0B
)

// Revision identifies one of the two hardware/firmware variants. They are
// told apart by the USB product string and differ in endpoints AND in the
// byte order of multi-byte packet fields.
type Revision int

const (
	// RevA: product "WinUSBComm", EP OUT data 0x01, cmd 0x02, IN 0x82.
	// Multi-byte fields are big-endian.
	RevA Revision = iota
	// RevB: product "WinUSBComm Device", EP OUT data 0x05, cmd 0x04, IN 0x85.
	// Multi-byte fields are little-endian.
	RevB
)

// Endpoints of a revision (numbers without the direction bit, as WebUSB
// and most libraries want them).
type Endpoints struct{ Cmd, DataOut, In int }

func (r Revision) Endpoints() Endpoints {
	if r == RevB {
		return Endpoints{Cmd: 4, DataOut: 5, In: 5}
	}
	return Endpoints{Cmd: 2, DataOut: 1, In: 2}
}

// RevisionFromProduct maps the USB product string to a revision,
// mirroring the vendor software.
func RevisionFromProduct(product string) (Revision, bool) {
	switch product {
	case "WinUSBComm":
		return RevA, true
	case "WinUSBComm Device":
		return RevB, true
	}
	return RevA, false
}

func (r Revision) String() string {
	if r == RevB {
		return "B (WinUSBComm Device, little-endian)"
	}
	return "A (WinUSBComm, big-endian)"
}

func (r Revision) order() binary.ByteOrder {
	if r == RevB {
		return binary.LittleEndian
	}
	return binary.BigEndian
}

// SPIClock is the SPI clock selection sent at packet offset 0x10.
type SPIClock uint8

const (
	Clock12MHz SPIClock = iota
	Clock6MHz
	Clock3MHz
	Clock1500kHz
	Clock750kHz
	Clock375kHz
)

var ClockNames = []string{"12 MHz", "6 MHz", "3 MHz", "1.5 MHz", "750 kHz", "375 kHz"}

// Packet is one 64-byte command.
type Packet [PacketSize]byte

func newPacket(flag, op byte) *Packet {
	p := &Packet{}
	p[0], p[1] = flag, op
	return p
}

func (p *Packet) put16(rev Revision, off int, v uint16) { rev.order().PutUint16(p[off:], v) }
func (p *Packet) put32(rev Revision, off int, v uint32) { rev.order().PutUint32(p[off:], v) }

// chipParams fills the fields shared by OpConfigure and OpDetect.
func (p *Packet) chipParams(rev Revision, c *Chip) {
	p[2] = byte(c.Protocol)
	p[3] = c.Sub
	p.put16(rev, 4, c.Page)
	p.put16(rev, 6, c.Timing)
	p.put32(rev, 8, c.Size)
	p.put32(rev, 0xC, c.JEDEC)
	p[0x1C] = byte(c.Voltage)
}

// ConfigurePacket builds the 0x07 session setup command.
func ConfigurePacket(rev Revision, c *Chip, clk SPIClock) *Packet {
	p := newPacket(0, OpConfigure)
	p.chipParams(rev, c)
	switch c.Protocol {
	case ProtoSPIFlash:
		p[0x10] = byte(clk)
	case ProtoSPI25:
		p[0x10] = byte(clk)<<4 + byte(c.Voltage)
	default:
		p[0x10] = byte(c.Voltage)
	}
	return p
}

// DetectPacket builds the 0x09 detection command.
func DetectPacket(rev Revision, c *Chip, clk SPIClock) *Packet {
	p := newPacket(0, OpDetect)
	p.chipParams(rev, c)
	p[0x10] = byte(clk)
	return p
}

// StreamPacket builds the 0x05 command that starts a data stream at addr.
func StreamPacket(rev Revision, addr uint32) *Packet {
	p := newPacket(0, OpStream)
	p.put32(rev, 8, addr)
	return p
}

// ErasePacket builds the 0x02 erase command with its u16 argument.
func ErasePacket(rev Revision, arg uint16) *Packet {
	p := newPacket(1, OpErase)
	p.put16(rev, 0x1A, arg)
	return p
}

// StatusPacket builds the 0x0A status poll.
func StatusPacket() *Packet { return newPacket(0, OpStatus) }

// EndSessionPacket builds the 0x08 end-of-session command.
func EndSessionPacket() *Packet { return newPacket(1, OpEndSession) }

// SPIPacket builds a raw SPI pass-through (max 60 bytes).
func SPIPacket(tx []byte) *Packet {
	p := newPacket(0, OpSPI)
	p[2] = 3
	p[3] = byte(len(tx))
	copy(p[4:], tx)
	return p
}

// EraseChipArg is the 0x1A argument requesting a full SPI flash chip erase.
const EraseChipArg = 0x8000

// Transport moves bytes to and from the programmer. Implementations exist
// for WebUSB (package webusb) and for tests (package ezpmock).
type Transport interface {
	Revision() Revision
	// WriteCommand sends a 64-byte packet to the command endpoint.
	WriteCommand(p []byte) error
	// WriteData sends bytes to the data OUT endpoint.
	WriteData(p []byte) error
	// Read reads up to n bytes from the IN endpoint. It may return fewer
	// (USB short packet); the caller loops as needed.
	Read(n int) ([]byte, error)
	Close() error
}
