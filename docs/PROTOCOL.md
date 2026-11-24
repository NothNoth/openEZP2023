# EZP2023+ USB protocol

Reverse-engineered statically from `EZP2023+.exe` v3.0 (C++Builder). The
USB layer lives in the `TUsbBulk` class, whose symbols are exported. No
hardware capture has confirmed this yet.

## Device

- VID `0x1FC8`, PID `0x310B`, vendor-specific class, bulk endpoints only.
  The vendor "driver" is a stock libusb-win32 1.2.6.
- Manufacturer string `www.zhifengsoft.com`.
- The product string selects the revision:

| Product | Cmd OUT | Data OUT | IN | Multi-byte fields |
|---|---|---|---|---|
| `WinUSBComm` | 0x02 | 0x01 | 0x82 | big-endian |
| `WinUSBComm Device` | 0x04 | 0x05 | 0x85 | little-endian |

- The host claims interface `bInterfaceNumber` of the first interface. It
  sends no `SET_CONFIGURATION` and no control transfers.
- Timeouts: 5 s for bulk transfers.

## Packets

Commands are 64 bytes on the command endpoint: `[0]` flag, `[1]` opcode,
everything else zero unless listed. When a reply is expected the host
waits 50 ms and reads 64 bytes from IN.

| Packet | Reply | Meaning |
|---|---|---|
| `00 07` + chip params | yes | Configure the session (reply ignored by the host) |
| `00 09` + chip params | yes | Detect: `r[0]` = 0 none, 1 SPI, 2 I²C, 3 Microwire; for SPI `r[1..3]` = JEDEC ID, manufacturer first |
| `00 05`, u32 `[8]` = address | read: yes, write: no | Start a data stream |
| `01 02`, u16 `[0x1A]` | no | Erase |
| `00 0A` | yes | Status, `r[0] & 1` = busy |
| `00 0B`, `[2]=3`, `[3]=n`, `[4..]` = bytes | yes | Raw SPI pass-through (only `9F` and `B7` seen) |
| `01 08` | no | End of session |

Chip parameters (`07` and `09`), from the database record:

| Offset | Width | Value |
|---|---|---|
| `[2]` | u8 | protocol |
| `[3]` | u8 | sub-type |
| `[4]` | u16 | page size |
| `[6]` | u16 | timing |
| `[8]` | u32 | chip size |
| `[0xC]` | u32 | JEDEC ID |
| `[0x10]` | u8 | `07`: SPI flash → clock index; 25xx → `clock<<4 + voltage`; others → voltage. `09`: clock index |
| `[0x1C]` | u8 | voltage |

Clock index: 0 = 12 MHz, 1 = 6 MHz, 2 = 3 MHz, 3 = 1.5 MHz, 4 = 750 kHz,
5 = 375 kHz.

## Sequences

Each vendor operation opens the device, runs `07`, does its work, sends
`01 08`, and closes. Read, write, erase, verify and blank check on SPI
flash, 24xx and 93xx first run a separate `09` + `01 08` and abort when
`r[0] == 0`.

- **Read** (also verify and blank check, compared on the host): if the
  chip is larger than 16 MB send `0B` with `B7`; send `05`(0) with reply;
  then read `max(page, 64)`-byte chunks until the chip size.
- **Write**: length = data without trailing `0xFF`, rounded up to a page.
  `B7` if needed, `05`(0) without reply, then page-sized writes on the data
  endpoint. Sleep 100 ms afterwards for 24xx.
- **Erase**:
  - SPI flash: `01 02` with `0x8000`, then every 50 ms poll `0A` while
    busy, up to `timing` polls.
  - 24xx, 25xx, 95xx: `01 02` with the byte address of each page.
  - 93xx: a single `01 02` with `timing`.

## Chip database (`EZP2023+.Dat`)

949 records of 68 bytes, little-endian:

| Offset | Field |
|---|---|
| `+0x00` | `TYPE,VENDOR,MODEL`, NUL-terminated, GBK |
| `+0x30` | u32 JEDEC ID (`0x00MMTTCC`), 0 for EEPROMs |
| `+0x34` | u32 size |
| `+0x38` | u16 page size |
| `+0x3A` | protocol: 0 SPI flash, 1 24xx, 2 93xx, 3 25xx, 4 95xx |
| `+0x3B` | sub-type: 25xx address width; 93xx organisation and address bits (`0x57` = 93C46 x16); set on some SST/M25P flashes |
| `+0x3C` | u16 timing: SPI flash erase timeout in 50 ms polls |
| `+0x40..42` | chip editor fields, not used by the host |
| `+0x43` | supply voltage: 0 = 3.3 V, 1 = 1.8 V, 2 = 5 V ("Working voltage" in the vendor chip editor) |

## To confirm with hardware

1. `lsusb -v -d 1fc8:310b`: product string, endpoints, interface number.
2. Whether the firmware cares about byte 0 (`00` vs `01`).
3. What the `07` and `0A` replies contain beyond what the host reads.
4. Whether larger bulk reads (for example 64 KB) work during a read stream.
5. How long the firmware tolerates a slow host during a stream.
6. Whether the voltage byte really switches VCC between 3.3 V and 5 V:
   measure VCC in the socket while reading a 24C02 (5 V) and a W25Q64 (3.3 V).

## Voltage

The programmer drives 3.3 V or 5 V. 1.8 V chips need the external 1.8 V
adapter (vendor manual). The database is inconsistent about them: 34 are
tagged "(1.8V)" in their name but stored as 3.3 V, 12 more (MX25U, GD25LQ40,
SST25WF) carry no marking at all, and only EN25LF40 has the 1.8 V byte.
`Chip.Needs18VAdapter()` combines the three signals.
