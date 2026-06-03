package widi

import "fmt"

// Product is a WIDI product (or OEM rebrand) addressable by its devID byte.
// Every product shares the same register map; only the devID differs.
type Product struct {
	Key   string // stable lowercase key used by tools (e.g. "thru6")
	Name  string // human name
	DevID byte
}

// Products lists the WIDI products this library knows how to address. All use
// the CME header; the Korg BM-1 (a different header) is intentionally omitted.
var Products = []Product{
	{"master", "WIDI Master", 0x09},
	{"uhost", "WIDI Uhost", 0x0A},
	{"jack", "WIDI Jack", 0x0B},
	{"xvive-md1", "Xvive MD1", 0x0C},
	{"kurzweil-airmidi", "Kurzweil AirMIDI", 0x0D},
	{"bud-pro", "WIDI Bud Pro", 0x0E},
	{"core", "WIDI Core", 0x0F},
	{"thru6", "WIDI Thru6 BT", 0x12},
	{"thru5-wc", "MIDI Thru5 WC", 0x18},
	{"widiflex-usb", "WIDIFLEX USB", 0x19},
	{"widiflex", "WIDIFLEX", 0x1B},
	{"ko2", "WIDI K.O.II", 0x1C},
}

// ProductByKey resolves a product by its stable key (e.g. "jack").
func ProductByKey(key string) (Product, bool) {
	for _, p := range Products {
		if p.Key == key {
			return p, true
		}
	}
	return Product{}, false
}

// ProductByDevID resolves a product by its devID byte.
func ProductByDevID(devID byte) (Product, bool) {
	for _, p := range Products {
		if p.DevID == devID {
			return p, true
		}
	}
	return Product{}, false
}

// ProductKeys returns the known product keys, in table order.
func ProductKeys() []string {
	keys := make([]string, len(Products))
	for i, p := range Products {
		keys[i] = p.Key
	}
	return keys
}

// ResolveDevID resolves a devID from a product key and/or an explicit devID.
// At least one must be supplied; if both are, they must agree.
func ResolveDevID(productKey string, devID int) (byte, error) {
	switch {
	case productKey != "" && devID >= 0:
		p, ok := ProductByKey(productKey)
		if !ok {
			return 0, fmt.Errorf("unknown product %q (known: %v)", productKey, ProductKeys())
		}
		if int(p.DevID) != devID {
			return 0, fmt.Errorf("product %q has devID 0x%02X, not 0x%02X", productKey, p.DevID, devID)
		}
		return p.DevID, nil
	case productKey != "":
		p, ok := ProductByKey(productKey)
		if !ok {
			return 0, fmt.Errorf("unknown product %q (known: %v)", productKey, ProductKeys())
		}
		return p.DevID, nil
	case devID >= 0:
		if devID > 0x7F {
			return 0, fmt.Errorf("devID 0x%02X out of range", devID)
		}
		return byte(devID), nil
	default:
		return 0, fmt.Errorf("provide a product key or a devID")
	}
}
