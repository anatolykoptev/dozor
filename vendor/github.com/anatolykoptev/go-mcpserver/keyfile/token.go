package keyfile

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"math/big"
	"regexp"
	"strings"
)

// Token layout: <prefix>1_<43 base62 chars of 32 random bytes><6 base62 chars
// of CRC-32/IEEE over everything before it>. Every token for a given prefix has
// the same length, and a malformed token is rejected before any hash lookup.
const (
	versionMarker = "1_"
	bodyLen       = 43 // base62 digits needed for 32 bytes (62^43 > 2^256)
	crcLen        = 6  // base62 digits needed for 32 bits (62^6 > 2^32)
	secretBytes   = 32
	base62        = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	base62Radix   = 62
)

var prefixRE = regexp.MustCompile(`^[a-z]{2,8}$`)

// Mint generates a fresh token for prefix and returns it together with the
// lowercase hex SHA-256 of the token, which is what the key file stores. The
// caller owns the token: it is never logged or retained here.
//
// Handling: write the token only to a file with mode 0600 (set the umask
// before redirecting output) and delete it once delivered. Add the hash to the
// key file by writing a temporary file with mode 0600 in the same directory and
// renaming it over the key file, so readers never see a half-written file.
func Mint(prefix string) (token, sha256hex string, err error) {
	if !prefixRE.MatchString(prefix) {
		return "", "", fmt.Errorf("keyfile: prefix must match %s", prefixRE)
	}
	var raw [secretBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", fmt.Errorf("keyfile: read random bytes: %w", err)
	}
	token = encodeToken(prefix, raw)
	return token, hashHex(token), nil
}

func hashHex(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func encodeToken(prefix string, raw [secretBytes]byte) string {
	head := prefix + versionMarker + encodeBase62(new(big.Int).SetBytes(raw[:]), bodyLen)
	return head + crcDigits(head)
}

// crcDigits is the fixed-width base62 rendering of the checksum of s.
func crcDigits(s string) string {
	return encodeBase62(new(big.Int).SetUint64(uint64(checksum([]byte(s)))), crcLen)
}

// checksum is CRC-32/IEEE (check value for "123456789" is 0xCBF43926).
func checksum(b []byte) uint32 { return crc32.ChecksumIEEE(b) }

// encodeBase62 renders n big-endian, left-padded with "0" to width digits.
func encodeBase62(n *big.Int, width int) string {
	digits := make([]byte, width)
	for i := range digits {
		digits[i] = base62[0]
	}
	radix, mod := big.NewInt(base62Radix), new(big.Int)
	for i := width - 1; i >= 0 && n.Sign() > 0; i-- {
		n.DivMod(n, radix, mod)
		digits[i] = base62[mod.Int64()]
	}
	return string(digits)
}

// wellFormed checks prefix, marker, length, alphabet and checksum. It touches
// no key material and performs no lookup.
func wellFormed(prefix, token string) bool {
	head := len(prefix) + len(versionMarker)
	if len(token) != head+bodyLen+crcLen || !strings.HasPrefix(token, prefix+versionMarker) {
		return false
	}
	for _, c := range []byte(token[head:]) {
		if strings.IndexByte(base62, c) < 0 {
			return false
		}
	}
	return token[head+bodyLen:] == crcDigits(token[:head+bodyLen])
}
