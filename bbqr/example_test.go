package bbqr_test

import (
	"fmt"

	"seedhammer.com/bbqr"
)

func Example() {
	// Encode any data as a BBQr series.
	s, err := bbqr.Split([]byte("seedhammer"), bbqr.TypeText, bbqr.SplitOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Printf("%d parts, QR version %d\n", len(s.Parts), s.Version)
	for _, p := range s.Parts {
		fmt.Println(p)
	}

	// Join accepts the parts in any order.
	typ, data, err := bbqr.Join(s.Parts)
	if err != nil {
		panic(err)
	}
	fmt.Printf("type %c: %s\n", typ, data)
	// Output:
	// 1 parts, QR version 5
	// B$2U0100ONSWKZDIMFWW2ZLS
	// type U: seedhammer
}

func ExampleDecoder() {
	s, _ := bbqr.Split([]byte("seedhammer"), bbqr.TypeBinary, bbqr.SplitOptions{})
	var d bbqr.Decoder
	for _, p := range s.Parts {
		if err := d.Add(p); err != nil {
			panic(err)
		}
	}
	typ, data, err := d.Result()
	if err != nil {
		panic(err)
	}
	fmt.Printf("type %c: %s\n", typ, data)
	// Output:
	// type B: seedhammer
}
