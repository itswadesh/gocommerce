// Command pincodes rebuilds the PIN code directory built into the engine
// (D135) from India Post's "All India Pincode Directory" CSV, which
// data.gov.in publishes under the Government Open Data Licence – India:
//
//	go run ./scripts/pincodes -in all_india_pincode_directory.csv
//
// It keeps one row a PIN (the district most of its post offices name, and the
// state) and writes core/postcodes_in.csv.gz. The same file in gives the same
// bytes out, so a rebuild with nothing new is no change to commit.
package main

import (
	"flag"
	"fmt"
	"os"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

func main() {
	in := flag.String("in", "", "India Post's directory CSV")
	out := flag.String("out", "core/postcodes_in.csv.gz", "where the compact directory is written")
	flag.Parse()
	if err := run(*in, *out); err != nil {
		fmt.Fprintln(os.Stderr, "pincodes:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	if in == "" {
		return fmt.Errorf("-in names the directory CSV")
	}
	d, err := gocommerce.LoadPostcodes(in)
	if err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	if err := d.WriteCompact(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("%d PIN codes written to %s\n", d.Len(), out)
	return nil
}
