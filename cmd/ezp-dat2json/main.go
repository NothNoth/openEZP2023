// Command ezp-dat2json converts the vendor chip database (EZP2023+.Dat,
// shipped with the Windows software) to the JSON format of ezp/chips.json.
//
//	go run ./cmd/ezp-dat2json EZP2023+.Dat > ezp/chips.json
package main

import (
	"fmt"
	"os"

	"ezp2023/ezp"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ezp-dat2json EZP2023+.Dat > chips.json")
		os.Exit(2)
	}
	b, err := os.ReadFile(os.Args[1])
	if err == nil {
		var chips []ezp.Chip
		if chips, err = ezp.ParseDat(b); err == nil {
			if b, err = ezp.MarshalChipsJSON(chips); err == nil {
				_, err = os.Stdout.Write(b)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
