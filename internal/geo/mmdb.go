// Package geo says which country an address is registered in, from a
// database file the site owner supplies.
//
// It reads the MaxMind DB file format, version 2, which the free country
// databases of DB-IP ("IP to Country Lite") and MaxMind ("GeoLite2 Country")
// both use. The reader was written from the published format description; it
// reads only what it needs (the country code) and treats the file as
// untrusted: every offset is checked and the work per lookup is bounded.
package geo

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// Code is a country code of two upper-case letters, such as "DE". The zero
// value means: not known.
type Code [2]byte

// String returns the code as text, or "" if it is not known.
func (c Code) String() string {
	if c == (Code{}) {
		return ""
	}
	return string(c[:])
}

// ParseCode reads a two-letter country code, in either case.
func ParseCode(s string) (Code, bool) { return parseCode([]byte(s)) }

// parseCode is ParseCode for text that is still in the file. The length is
// checked before anything is copied: the text comes from an untrusted file.
func parseCode(s []byte) (Code, bool) {
	if len(s) != 2 {
		return Code{}, false
	}
	var c Code
	for i := 0; i < 2; i++ {
		b := s[i]
		if b >= 'a' && b <= 'z' {
			b -= 'a' - 'A'
		}
		if b < 'A' || b > 'Z' {
			return Code{}, false
		}
		c[i] = b
	}
	return c, true
}

// Limits when reading a database.
const (
	maxMetadata = 128 << 10 // per the format description
	// maxWork bounds how many values one lookup may touch. A country record
	// has a few dozen; a crafted file cannot make a lookup expensive.
	maxWork  = 4096
	maxDepth = 32
)

var marker = []byte("\xab\xcd\xefMaxMind.com")

// Data types of the format.
const (
	typePointer = 1
	typeString  = 2
	typeMap     = 7
	typeArray   = 11
)

// DB is a database held in memory. It is read-only and safe for concurrent use.
type DB struct {
	tree      []byte
	data      []byte // the data section
	nodeCount uint32
	record    int // bits per record: 24, 28 or 32
	ipv6      bool
	ipv4Start uint32 // node where the IPv4 addresses start in an IPv6 tree

	// Type is the database's own name for what it holds.
	Type string
	// Built is when the database was made.
	Built time.Time
}

// errCorrupt is returned for a lookup that runs into broken data.
var errCorrupt = errors.New("the database is damaged")

// Open checks data and returns the database in it. data must not be changed
// afterwards.
func Open(data []byte) (*DB, error) {
	start := len(data) - maxMetadata
	if start < 0 {
		start = 0
	}
	at := bytes.LastIndex(data[start:], marker)
	if at < 0 {
		return nil, errors.New("it is not a database in MaxMind DB format (.mmdb)")
	}
	metaStart := start + at
	meta := section(data[metaStart+len(marker):])

	db := &DB{}
	number := func(key string) (uint64, error) {
		work := maxWork
		off, found, err := meta.get(0, key, &work)
		if err != nil || !found {
			return 0, fmt.Errorf("its description lacks %q", key)
		}
		return meta.uint(off)
	}
	nodes, err := number("node_count")
	if err != nil {
		return nil, err
	}
	record, err := number("record_size")
	if err != nil {
		return nil, err
	}
	version, err := number("ip_version")
	if err != nil {
		return nil, err
	}
	major, err := number("binary_format_major_version")
	if err != nil {
		return nil, err
	}
	if major != 2 {
		return nil, fmt.Errorf("it has format version %d; only version 2 can be read", major)
	}
	if record != 24 && record != 28 && record != 32 {
		return nil, fmt.Errorf("it has a record size of %d bits; 24, 28 and 32 can be read", record)
	}
	if version != 4 && version != 6 {
		return nil, fmt.Errorf("it says IP version %d", version)
	}
	if nodes == 0 || nodes > 1<<31 {
		return nil, errors.New("its size information is not plausible")
	}
	treeSize := nodes * (record * 2 / 8)
	if treeSize+16 > uint64(metaStart) {
		return nil, errors.New("it is cut off or its size information is wrong")
	}
	db.nodeCount, db.record, db.ipv6 = uint32(nodes), int(record), version == 6
	db.tree = data[:treeSize]
	db.data = data[treeSize+16 : metaStart]

	work := maxWork
	if off, found, err := meta.get(0, "database_type", &work); err == nil && found {
		if s, err := meta.str(off); err == nil && len(s) <= 100 {
			db.Type = string(s)
		}
	}
	if epoch, err := number("build_epoch"); err == nil && epoch < 1<<40 {
		db.Built = time.Unix(int64(epoch), 0).UTC()
	}

	if db.ipv6 {
		node := uint32(0)
		for i := 0; i < 96 && node < db.nodeCount; i++ {
			node = db.read(node, 0)
		}
		db.ipv4Start = node
	}
	return db, nil
}

// read returns the left (bit 0) or right (bit 1) record of a node. node must
// be below nodeCount.
func (db *DB) read(node uint32, bit byte) uint32 {
	switch db.record {
	case 24:
		b := db.tree[int(node)*6+int(bit)*3:]
		return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
	case 28:
		b := db.tree[int(node)*7:]
		if bit == 0 {
			return uint32(b[3]>>4)<<24 | uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
		}
		return uint32(b[3]&0x0f)<<24 | uint32(b[4])<<16 | uint32(b[5])<<8 | uint32(b[6])
	default:
		b := db.tree[int(node)*8+int(bit)*4:]
		return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	}
}

// Country returns the country an address is registered in, or the zero Code
// if the database does not know. It does not allocate.
func (db *DB) Country(addr netip.Addr) Code {
	addr = addr.Unmap().WithZone("")
	if !addr.IsValid() {
		return Code{}
	}
	raw := addr.As16()
	bits := raw[:]
	node := uint32(0)
	if addr.Is4() {
		bits = raw[12:]
		if db.ipv6 {
			node = db.ipv4Start
		}
	} else if !db.ipv6 {
		return Code{} // an IPv6 address in a database of IPv4 addresses
	}
	for i := 0; i < len(bits)*8 && node < db.nodeCount; i++ {
		node = db.read(node, bits[i/8]>>(7-i%8)&1)
	}
	if node <= db.nodeCount {
		return Code{} // no data for this address, or the tree is deeper than an address
	}
	off := int(node-db.nodeCount) - 16
	if off < 0 || off >= len(db.data) {
		return Code{}
	}
	code, _ := db.country(off)
	return code
}

// country reads the country code from the record at off. Databases name it
// differently; the layouts in use are tried in turn:
//
//	country.iso_code   DB-IP Lite, MaxMind GeoLite2 and GeoIP2
//	country_code       IPinfo and others
//	country            a plain two-letter text
func (db *DB) country(off int) (Code, error) {
	d := section(db.data)
	work := maxWork
	value, found, err := d.get(off, "country", &work)
	if err != nil {
		return Code{}, err
	}
	if found {
		if s, err := d.str(value); err == nil {
			if code, ok := parseCode(s); ok {
				return code, nil
			}
		} else if iso, found, err := d.get(value, "iso_code", &work); err == nil && found {
			if s, err := d.str(iso); err == nil {
				if code, ok := parseCode(s); ok {
					return code, nil
				}
			}
		}
	}
	if value, found, err := d.get(off, "country_code", &work); err == nil && found {
		if s, err := d.str(value); err == nil {
			if code, ok := parseCode(s); ok {
				return code, nil
			}
		}
	}
	return Code{}, nil
}

// section is a part of the file in which pointers are resolved: the data
// section, or the metadata.
type section []byte

// field reads the control bytes at off: the type, the size, and where the
// payload starts. For a pointer, size is the offset it points to.
func (s section) field(off int) (typ, size, next int, err error) {
	if off < 0 || off >= len(s) {
		return 0, 0, 0, errCorrupt
	}
	ctrl := s[off]
	off++
	typ = int(ctrl >> 5)
	if typ == typePointer {
		n := int(ctrl>>3) & 3
		if off+n+1 > len(s) {
			return 0, 0, 0, errCorrupt
		}
		var v int
		switch n {
		case 0:
			v = int(ctrl&7)<<8 | int(s[off])
		case 1:
			v = (int(ctrl&7)<<16 | int(s[off])<<8 | int(s[off+1])) + 2048
		case 2:
			v = (int(ctrl&7)<<24 | int(s[off])<<16 | int(s[off+1])<<8 | int(s[off+2])) + 526336
		default:
			v = int(s[off])<<24 | int(s[off+1])<<16 | int(s[off+2])<<8 | int(s[off+3])
		}
		return typePointer, v, off + n + 1, nil
	}
	if typ == 0 { // extended type
		if off >= len(s) {
			return 0, 0, 0, errCorrupt
		}
		typ = int(s[off]) + 7
		off++
	}
	size = int(ctrl & 0x1f)
	extra := 0
	switch size {
	case 29:
		extra = 1
	case 30:
		extra = 2
	case 31:
		extra = 3
	}
	if off+extra > len(s) {
		return 0, 0, 0, errCorrupt
	}
	switch extra {
	case 1:
		size = 29 + int(s[off])
	case 2:
		size = 285 + (int(s[off])<<8 | int(s[off+1]))
	case 3:
		size = 65821 + (int(s[off])<<16 | int(s[off+1])<<8 | int(s[off+2]))
	}
	return typ, size, off + extra, nil
}

// resolve reads the field at off and, if it is a pointer, the field it
// points to. A pointer to a pointer is not allowed by the format.
func (s section) resolve(off int) (typ, size, next int, err error) {
	typ, size, next, err = s.field(off)
	if err != nil || typ != typePointer {
		return
	}
	typ, size, next, err = s.field(size)
	if err == nil && typ == typePointer {
		err = errCorrupt
	}
	return
}

// str returns the text at off.
func (s section) str(off int) ([]byte, error) {
	typ, size, next, err := s.resolve(off)
	if err != nil {
		return nil, err
	}
	if typ != typeString || next+size > len(s) {
		return nil, errCorrupt
	}
	return s[next : next+size], nil
}

// uint returns the unsigned number at off.
func (s section) uint(off int) (uint64, error) {
	typ, size, next, err := s.resolve(off)
	if err != nil {
		return 0, err
	}
	if (typ != 5 && typ != 6 && typ != 9) || size > 8 || next+size > len(s) {
		return 0, errCorrupt
	}
	var v uint64
	for _, b := range s[next : next+size] {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

// get looks for key in the map at off and returns where its value starts.
// Values of other keys are stepped over without following their pointers.
func (s section) get(off int, key string, work *int) (value int, found bool, err error) {
	typ, pairs, next, err := s.resolve(off)
	if err != nil {
		return 0, false, err
	}
	if typ != typeMap {
		return 0, false, nil
	}
	for i := 0; i < pairs; i++ {
		if *work--; *work < 0 {
			return 0, false, errCorrupt
		}
		name, err := s.str(next)
		if err != nil {
			return 0, false, err
		}
		if next, err = s.skip(next, work, 0); err != nil { // step over the key
			return 0, false, err
		}
		if string(name) == key {
			return next, true, nil
		}
		if next, err = s.skip(next, work, 0); err != nil { // step over the value
			return 0, false, err
		}
	}
	return 0, false, nil
}

// skip returns where the field after the one at off starts.
func (s section) skip(off int, work *int, depth int) (int, error) {
	if *work--; *work < 0 || depth > maxDepth {
		return 0, errCorrupt
	}
	typ, size, next, err := s.field(off)
	if err != nil {
		return 0, err
	}
	switch typ {
	case typePointer:
		return next, nil
	case typeMap:
		size *= 2
		fallthrough
	case typeArray:
		for i := 0; i < size; i++ {
			if next, err = s.skip(next, work, depth+1); err != nil {
				return 0, err
			}
		}
		return next, nil
	case 14: // boolean: the size is the value
		return next, nil
	default:
		if next+size > len(s) {
			return 0, errCorrupt
		}
		return next + size, nil
	}
}
