package minerva

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
)

const maxBencodeDepth = 64

type bkind uint8

const (
	bstring bkind = iota
	binteger
	blist
	bdict
)

type bvalue struct {
	kind    bkind
	string  []byte
	integer int64
	list    []bvalue
	dict    []bdictEntry
}

type bdictEntry struct {
	key        []byte
	value      bvalue
	valueStart int
	valueEnd   int
}

type bdecoder struct {
	data []byte
	pos  int
}

func decodeBencode(data []byte) (bvalue, error) {
	d := bdecoder{data: data}
	v, err := d.value(0)
	if err != nil {
		return bvalue{}, err
	}
	if d.pos != len(data) {
		return bvalue{}, fmt.Errorf("bencode: trailing data at byte %d", d.pos)
	}
	return v, nil
}

func (d *bdecoder) value(depth int) (bvalue, error) {
	if d.pos >= len(d.data) {
		return bvalue{}, errors.New("bencode: unexpected end of data")
	}

	switch d.data[d.pos] {
	case 'i':
		return d.integerValue()
	case 'l':
		if depth >= maxBencodeDepth {
			return bvalue{}, fmt.Errorf("bencode: nesting exceeds %d", maxBencodeDepth)
		}
		return d.listValue(depth + 1)
	case 'd':
		if depth >= maxBencodeDepth {
			return bvalue{}, fmt.Errorf("bencode: nesting exceeds %d", maxBencodeDepth)
		}
		return d.dictValue(depth + 1)
	default:
		if d.data[d.pos] < '0' || d.data[d.pos] > '9' {
			return bvalue{}, fmt.Errorf("bencode: invalid value at byte %d", d.pos)
		}
		s, err := d.stringValue()
		if err != nil {
			return bvalue{}, err
		}
		return bvalue{kind: bstring, string: s}, nil
	}
}

func (d *bdecoder) integerValue() (bvalue, error) {
	start := d.pos
	d.pos++ // i
	digitsStart := d.pos
	for d.pos < len(d.data) && d.data[d.pos] != 'e' {
		c := d.data[d.pos]
		if (c < '0' || c > '9') && !(d.pos == digitsStart && c == '-') {
			return bvalue{}, fmt.Errorf("bencode: invalid integer at byte %d", start)
		}
		d.pos++
	}
	if d.pos == len(d.data) {
		return bvalue{}, fmt.Errorf("bencode: unterminated integer at byte %d", start)
	}
	digits := d.data[digitsStart:d.pos]
	if len(digits) == 0 || bytes.Equal(digits, []byte("-")) ||
		(digits[0] == '0' && len(digits) > 1) ||
		(len(digits) > 1 && digits[0] == '-' && digits[1] == '0') {
		return bvalue{}, fmt.Errorf("bencode: non-canonical integer at byte %d", start)
	}
	n, err := strconv.ParseInt(string(digits), 10, 64)
	if err != nil {
		return bvalue{}, fmt.Errorf("bencode: invalid integer at byte %d: %w", start, err)
	}
	d.pos++ // e
	return bvalue{kind: binteger, integer: n}, nil
}

func (d *bdecoder) stringValue() ([]byte, error) {
	start := d.pos
	for d.pos < len(d.data) && d.data[d.pos] != ':' {
		if d.data[d.pos] < '0' || d.data[d.pos] > '9' {
			return nil, fmt.Errorf("bencode: invalid string length at byte %d", start)
		}
		d.pos++
	}
	if d.pos == len(d.data) {
		return nil, fmt.Errorf("bencode: unterminated string length at byte %d", start)
	}
	digits := d.data[start:d.pos]
	if len(digits) == 0 || (len(digits) > 1 && digits[0] == '0') {
		return nil, fmt.Errorf("bencode: non-canonical string length at byte %d", start)
	}
	n, err := strconv.ParseUint(string(digits), 10, 64)
	if err != nil || n > uint64(len(d.data)-d.pos-1) {
		return nil, fmt.Errorf("bencode: invalid string length at byte %d", start)
	}
	d.pos++ // :
	end := d.pos + int(n)
	s := d.data[d.pos:end]
	d.pos = end
	return s, nil
}

func (d *bdecoder) listValue(depth int) (bvalue, error) {
	d.pos++ // l
	values := make([]bvalue, 0)
	for {
		if d.pos >= len(d.data) {
			return bvalue{}, errors.New("bencode: unterminated list")
		}
		if d.data[d.pos] == 'e' {
			d.pos++
			return bvalue{kind: blist, list: values}, nil
		}
		v, err := d.value(depth)
		if err != nil {
			return bvalue{}, err
		}
		values = append(values, v)
	}
}

func (d *bdecoder) dictValue(depth int) (bvalue, error) {
	d.pos++ // d
	entries := make([]bdictEntry, 0)
	var previous []byte
	for {
		if d.pos >= len(d.data) {
			return bvalue{}, errors.New("bencode: unterminated dictionary")
		}
		if d.data[d.pos] == 'e' {
			d.pos++
			return bvalue{kind: bdict, dict: entries}, nil
		}
		if d.data[d.pos] < '0' || d.data[d.pos] > '9' {
			return bvalue{}, fmt.Errorf("bencode: dictionary key is not a string at byte %d", d.pos)
		}
		key, err := d.stringValue()
		if err != nil {
			return bvalue{}, err
		}
		if previous != nil && bytes.Compare(previous, key) >= 0 {
			return bvalue{}, fmt.Errorf("bencode: dictionary keys are not strictly ordered at byte %d", d.pos)
		}
		valueStart := d.pos
		value, err := d.value(depth)
		if err != nil {
			return bvalue{}, err
		}
		entries = append(entries, bdictEntry{key: key, value: value, valueStart: valueStart, valueEnd: d.pos})
		previous = key
	}
}
