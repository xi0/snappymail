package cryptox

import (
	"encoding/binary"
	"errors"
	"math"
)

// This file ports MailSo\Base\Xxtea. It is a byte for byte port: the algorithm
// works on little-endian uint32 words and relies on 32-bit wrapping arithmetic,
// and the final block of the encrypt loop uses p == n while the decrypt loop
// uses p == 0, exactly like the PHP source.

// errXxteaCorrupt reports a payload whose length marker is out of range (PHP's
// long2str() returning false).
var errXxteaCorrupt = errors.New("cryptox: corrupt xxtea payload")

// xxteaDelta is the XXTEA golden ratio constant.
const xxteaDelta = uint32(0x9E3779B9)

// xxteaEncrypt is MailSo\Base\Xxtea::Encrypt().
func xxteaEncrypt(plaintext, key string) (string, error) {
	values := xxteaStr2long(plaintext, true)
	words := xxteaStr2long(key, false)
	for len(words) < 4 {
		words = append(words, 0)
	}

	n := len(values) - 1
	if 0 > n {
		return "", errXxteaCorrupt
	}

	z := values[n]
	y := values[0]
	rounds := int(math.Floor(6 + 52/float64(n+1)))
	sum := uint32(0)

	for ; 0 < rounds; rounds-- {
		sum += xxteaDelta
		round := (sum >> 2) & 3

		position := 0
		for ; position < n; position++ {
			y = values[position+1]
			z = values[position] + xxteaMix(z, y, sum, words[(position&3)^int(round)])
			values[position] = z
		}

		// position == n here, like the leftover loop variable in PHP.
		y = values[0]
		z = values[n] + xxteaMix(z, y, sum, words[(position&3)^int(round)])
		values[n] = z
	}

	return xxteaLong2str(values, false)
}

// xxteaDecrypt is MailSo\Base\Xxtea::Decrypt().
func xxteaDecrypt(ciphertext, key string) (string, error) {
	values := xxteaStr2long(ciphertext, false)
	words := xxteaStr2long(key, false)
	for len(words) < 4 {
		words = append(words, 0)
	}

	n := len(values) - 1
	if 0 > n {
		return "", errXxteaCorrupt
	}

	z := values[n]
	y := values[0]
	rounds := int(math.Floor(6 + 52/float64(n+1)))
	sum := uint32(rounds) * xxteaDelta

	for 0 != sum {
		round := (sum >> 2) & 3

		position := n
		for ; 0 < position; position-- {
			z = values[position-1]
			y = values[position] - xxteaMix(z, y, sum, words[(position&3)^int(round)])
			values[position] = y
		}

		// position == 0 here, like the leftover loop variable in PHP.
		z = values[n]
		y = values[0] - xxteaMix(z, y, sum, words[(position&3)^int(round)])
		values[0] = y

		sum -= xxteaDelta
	}

	return xxteaLong2str(values, true)
}

// xxteaMix computes the MX expression of the reference implementation. The
// masks in the PHP source (& 0x07ffffff, & 0x1fffffff) are no-ops for a 32-bit
// value, and uint32 arithmetic reproduces PHP's int32() wrapping.
func xxteaMix(z, y, sum, key uint32) uint32 {
	return ((z>>5 ^ y<<2) + (y>>3 ^ z<<4)) ^ ((sum ^ y) + (key ^ z))
}

// xxteaStr2long is str2long(): little-endian words, zero padded to a multiple
// of four bytes, optionally followed by the original byte length.
func xxteaStr2long(value string, includeLength bool) []uint32 {
	padding := (4 - len(value)%4) & 3
	data := make([]byte, len(value)+padding)
	copy(data, value)

	words := make([]uint32, 0, len(data)/4+1)
	for index := 0; index < len(data); index += 4 {
		words = append(words, binary.LittleEndian.Uint32(data[index:index+4]))
	}

	if includeLength {
		words = append(words, uint32(len(value)))
	}

	return words
}

// xxteaLong2str is long2str(): little-endian bytes, truncated to the length
// marker when includeLength is set. An out of range marker is an error (PHP
// returns false).
func xxteaLong2str(words []uint32, includeLength bool) (string, error) {
	if 0 == len(words) {
		return "", errXxteaCorrupt
	}

	length := len(words)
	end := (length - 1) << 2

	if includeLength {
		marker := int64(words[length-1])
		if marker < int64(end-3) || marker > int64(end) {
			return "", errXxteaCorrupt
		}
		end = int(marker)
	}

	buffer := make([]byte, 4*length)
	for index, word := range words {
		binary.LittleEndian.PutUint32(buffer[index*4:], word)
	}

	if includeLength {
		return string(buffer[:end]), nil
	}
	return string(buffer), nil
}
