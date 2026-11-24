package ezp

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Protocol is the bus family of a chip (record byte +0x3A).
type Protocol uint8

const (
	ProtoSPIFlash  Protocol = 0 // 25xx SPI NOR flash
	ProtoI2C24     Protocol = 1 // 24xx I²C EEPROM
	ProtoMicrowire Protocol = 2 // 93xx Microwire EEPROM
	ProtoSPI25     Protocol = 3 // 25xx SPI EEPROM
	ProtoSPI95     Protocol = 4 // 95xx SPI EEPROM
)

func (p Protocol) String() string {
	switch p {
	case ProtoSPIFlash:
		return "SPI flash"
	case ProtoI2C24:
		return "24xx I²C EEPROM"
	case ProtoMicrowire:
		return "93xx Microwire EEPROM"
	case ProtoSPI25:
		return "25xx SPI EEPROM"
	case ProtoSPI95:
		return "95xx SPI EEPROM"
	}
	return fmt.Sprintf("protocol %d", uint8(p))
}

// Voltage is the supply voltage of a chip (record byte +0x43), as chosen in
// the vendor chip editor. It is sent to the firmware with every session.
type Voltage uint8

const (
	Volt3V3 Voltage = 0
	Volt1V8 Voltage = 1
	Volt5V  Voltage = 2
)

func (v Voltage) String() string {
	switch v {
	case Volt3V3:
		return "3.3 V"
	case Volt1V8:
		return "1.8 V"
	case Volt5V:
		return "5 V"
	}
	return fmt.Sprintf("voltage %d", uint8(v))
}

// RecordSize is the size of one chip record in EZP2023+.Dat.
const RecordSize = 68

// Chip is one entry of the vendor chip database.
//
// Layout of a record (little-endian):
//
//	+0x00 [48]byte  "TYPE,VENDOR,MODEL", NUL-terminated, GBK
//	+0x30 u32       JEDEC ID (manufacturer in bits 16..23), 0 for EEPROMs
//	+0x34 u32       size in bytes
//	+0x38 u16       page size
//	+0x3A u8        protocol
//	+0x3B u8        sub-type (address width / 93xx organisation / SST quirk)
//	+0x3C u16       timing (SPI flash: erase timeout in 50 ms ticks)
//	+0x3E u16       unused by the host
//	+0x40 [3]byte   editor fields not used by the host
//	+0x43 u8        supply voltage (0 = 3.3 V, 1 = 1.8 V, 2 = 5 V)
type Chip struct {
	Index    int
	Type     string
	Vendor   string
	Model    string
	JEDEC    uint32
	Size     uint32
	Page     uint16
	Protocol Protocol
	Sub      uint8
	Timing   uint16
	Voltage  Voltage
	Flags    [3]byte // +0x40..+0x42, not used by the host
}

// prefixes18V are 1.8 V SPI flash families that the vendor database does
// not mark as such (neither in the voltage byte nor in the name).
// (No regexp: the package adds about 500 KB to the WebAssembly build.)
var prefixes18V = []string{
	"MX25U", "MX25R", "GD25LQ", "GD25LB", "GD25LE", "SST25WF", "SST26WF",
	"ZB25LQ", "FM25M", "EN25S", "IS25W", "AT25SL", "MT25QU", "XM25QU",
}

// is18VModel reports whether an upper-case model name belongs to a known
// 1.8 V family.
func is18VModel(m string) bool {
	for _, p := range prefixes18V {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	// Winbond: W25Q<size>[letter]<B|D|E|F|J>W, e.g. W25Q64FW, W25Q16DW.
	if rest, ok := strings.CutPrefix(m, "W25Q"); ok {
		rest = strings.TrimLeft(rest, "0123456789")
		if len(rest) >= 3 && rest[0] >= 'A' && rest[0] <= 'Z' && strings.ContainsRune("BDEFJ", rune(rest[1])) && rest[2] == 'W' {
			return true
		}
		return len(rest) >= 2 && strings.ContainsRune("BDEFJ", rune(rest[0])) && rest[1] == 'W'
	}
	// BoyaMicro: BY25Q<size><letters>L, e.g. BY25Q40AL.
	if rest, ok := strings.CutPrefix(m, "BY25Q"); ok {
		rest = strings.TrimLeft(rest, "0123456789")
		letters := strings.TrimLeft(rest, "ABCDEFGHIJKMNOPQRSTUVWXYZ") // every letter but L
		return strings.HasPrefix(letters, "L")
	}
	return false
}

// Needs18VAdapter reports whether the chip runs at 1.8 V. The programmer
// itself drives 3.3 V or 5 V, so such chips must go through the 1.8 V
// adapter or they may be damaged. Three sources are combined because the
// vendor database is inconsistent: the voltage byte, a "(1.8V)" tag in the
// model name, and a list of known 1.8 V families.
func (c *Chip) Needs18VAdapter() bool {
	return c.Voltage == Volt1V8 ||
		strings.Contains(strings.ToUpper(c.Model), "1.8V") ||
		is18VModel(strings.ToUpper(c.Model))
}

func (c *Chip) String() string {
	return fmt.Sprintf("%s %s (%s, %d bytes)", c.Vendor, c.Model, c.Protocol, c.Size)
}

// ParseDat decodes the whole EZP2023+.Dat file. Empty records are skipped.
func ParseDat(b []byte) ([]Chip, error) {
	if len(b)%RecordSize != 0 {
		return nil, fmt.Errorf("ezp: .Dat size %d is not a multiple of %d", len(b), RecordSize)
	}
	var chips []Chip
	for i := 0; i+RecordSize <= len(b); i += RecordSize {
		c, ok := parseRecord(b[i:i+RecordSize], i/RecordSize)
		if ok {
			chips = append(chips, c)
		}
	}
	return chips, nil
}

func parseRecord(r []byte, index int) (Chip, bool) {
	name := decodeName(r[:0x30])
	if name == "" {
		return Chip{}, false
	}
	parts := strings.SplitN(name, ",", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	le := binary.LittleEndian
	c := Chip{
		Index:    index,
		Type:     parts[0],
		Vendor:   parts[1],
		Model:    parts[2],
		JEDEC:    le.Uint32(r[0x30:]),
		Size:     le.Uint32(r[0x34:]),
		Page:     le.Uint16(r[0x38:]),
		Protocol: Protocol(r[0x3A]),
		Sub:      r[0x3B],
		Timing:   le.Uint16(r[0x3C:]),
		Voltage:  Voltage(r[0x43]),
	}
	copy(c.Flags[:], r[0x40:0x43])
	return c, true
}

// decodeName handles the only non-ASCII sequences present in the vendor
// database (full-width parentheses in GBK) without pulling in x/text.
func decodeName(b []byte) string {
	var sb strings.Builder
	for i := 0; i < len(b) && b[i] != 0; i++ {
		if b[i] < 0x80 {
			sb.WriteByte(b[i])
			continue
		}
		if i+1 < len(b) && b[i] == 0xA3 {
			switch b[i+1] {
			case 0xA8:
				sb.WriteByte('(')
			case 0xA9:
				sb.WriteByte(')')
			default:
				sb.WriteByte('?')
			}
			i++
			continue
		}
		sb.WriteByte('?')
	}
	return sb.String()
}

// FindByJEDEC returns all SPI flash chips matching a JEDEC ID.
func FindByJEDEC(chips []Chip, id uint32) []Chip {
	var out []Chip
	for _, c := range chips {
		if c.Protocol == ProtoSPIFlash && c.JEDEC == id {
			out = append(out, c)
		}
	}
	return out
}
