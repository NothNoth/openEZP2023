package ezp

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
)

// chipsJSON is the built-in chip database. One JSON object per line keeps
// diffs readable; see chipJSON for the fields.
//
//go:embed chips.json
var chipsJSON []byte

var builtin = sync.OnceValues(func() ([]Chip, error) { return ParseChipsJSON(chipsJSON) })

// Chips returns a copy of the built-in chip database.
func Chips() ([]Chip, error) {
	c, err := builtin()
	if err != nil {
		return nil, err
	}
	return append([]Chip(nil), c...), nil
}

// chipJSON is the on-disk form of a Chip. Index is kept explicitly: the UI
// stores it to remember the selected chip.
type chipJSON struct {
	Index      int      `json:"index"`
	Type       string   `json:"type"`
	Vendor     string   `json:"vendor"`
	Model      string   `json:"model"`
	JEDEC      string   `json:"jedec,omitempty"` // hex, e.g. "EF4018"
	Size       uint32   `json:"size"`
	Page       uint16   `json:"page"`
	Protocol   Protocol `json:"protocol"`
	Sub        uint8    `json:"sub"`
	Timing     uint16   `json:"timing"`
	Voltage    Voltage  `json:"voltage"`
	Extend     uint16   `json:"extend"`
	EEPROM     uint16   `json:"eeprom"`
	EEPROMPage uint8    `json:"eepromPage"`
}

var protocolNames = map[Protocol]string{
	ProtoSPIFlash:  "spi-flash",
	ProtoI2C24:     "24xx",
	ProtoMicrowire: "93xx",
	ProtoSPI25:     "25xx",
	ProtoSPI95:     "95xx",
}

var voltageNames = map[Voltage]string{
	Volt3V3: "3.3V",
	Volt1V8: "1.8V",
	Volt5V:  "5V",
}

func (p Protocol) MarshalText() ([]byte, error) {
	if s, ok := protocolNames[p]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("ezp: unknown protocol %d", uint8(p))
}

func (p *Protocol) UnmarshalText(b []byte) error {
	for k, v := range protocolNames {
		if v == string(b) {
			*p = k
			return nil
		}
	}
	return fmt.Errorf("ezp: unknown protocol %q", b)
}

func (v Voltage) MarshalText() ([]byte, error) {
	if s, ok := voltageNames[v]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("ezp: unknown voltage %d", uint8(v))
}

func (v *Voltage) UnmarshalText(b []byte) error {
	for k, s := range voltageNames {
		if s == string(b) {
			*v = k
			return nil
		}
	}
	return fmt.Errorf("ezp: unknown voltage %q", b)
}

// ParseChipsJSON decodes a chip database in the format of chips.json.
func ParseChipsJSON(b []byte) ([]Chip, error) {
	var in []chipJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("ezp: chip database: %w", err)
	}
	chips := make([]Chip, 0, len(in))
	seen := make(map[int]bool, len(in))
	for _, j := range in {
		if seen[j.Index] {
			return nil, fmt.Errorf("ezp: chip database: duplicate index %d", j.Index)
		}
		seen[j.Index] = true
		var jedec uint64
		if j.JEDEC != "" {
			var err error
			if jedec, err = strconv.ParseUint(j.JEDEC, 16, 32); err != nil {
				return nil, fmt.Errorf("ezp: chip database: %s: bad JEDEC %q", j.Model, j.JEDEC)
			}
		}
		chips = append(chips, Chip{
			Index: j.Index, Type: j.Type, Vendor: j.Vendor, Model: j.Model,
			JEDEC: uint32(jedec), Size: j.Size, Page: j.Page,
			Protocol: j.Protocol, Sub: j.Sub, Timing: j.Timing, Voltage: j.Voltage,
			Extend: j.Extend, EEPROM: j.EEPROM, EEPROMPage: j.EEPROMPage,
		})
	}
	return chips, nil
}

// MarshalChipsJSON encodes chips in the format of chips.json: a JSON array
// with one chip per line.
func MarshalChipsJSON(chips []Chip) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("[\n")
	for i, c := range chips {
		j := chipJSON{
			Index: c.Index, Type: c.Type, Vendor: c.Vendor, Model: c.Model,
			Size: c.Size, Page: c.Page,
			Protocol: c.Protocol, Sub: c.Sub, Timing: c.Timing, Voltage: c.Voltage,
			Extend: c.Extend, EEPROM: c.EEPROM, EEPROMPage: c.EEPROMPage,
		}
		if c.JEDEC != 0 {
			j.JEDEC = fmt.Sprintf("%06X", c.JEDEC)
		}
		line, err := json.Marshal(j)
		if err != nil {
			return nil, err
		}
		buf.WriteString("  ")
		buf.Write(line)
		if i < len(chips)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("]\n")
	return buf.Bytes(), nil
}
