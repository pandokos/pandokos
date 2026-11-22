package password

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

var errFormat = errors.New("password: not a supported hash")

var b64 = base64.RawStdEncoding

func encode(p Params, salt, key []byte) string {
	return "$argon2id$v=19$m=" + strconv.FormatUint(uint64(p.MemoryKiB), 10) +
		",t=" + strconv.FormatUint(uint64(p.Iterations), 10) +
		",p=" + strconv.FormatUint(uint64(p.Parallelism), 10) +
		"$" + b64.EncodeToString(salt) + "$" + b64.EncodeToString(key)
}

// parse reads exactly the format that encode writes; every bound is checked before anything is
// allocated or computed.
func parse(s string) (p Params, salt, key []byte, err error) {
	if len(s) > 512 {
		return p, nil, nil, errFormat
	}
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return p, nil, nil, errFormat
	}
	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return p, nil, nil, errFormat
	}
	m, ok1 := number(fields[0], "m=", maxMemoryKiB)
	t, ok2 := number(fields[1], "t=", maxIterations)
	par, ok3 := number(fields[2], "p=", maxParallelism)
	if !ok1 || !ok2 || !ok3 {
		return p, nil, nil, errFormat
	}
	if !alphabetOnly(parts[4]) || !alphabetOnly(parts[5]) {
		return p, nil, nil, errFormat
	}
	salt, err = b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return p, nil, nil, errFormat
	}
	key, err = b64.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return p, nil, nil, errFormat
	}
	return Params{MemoryKiB: uint32(m), Iterations: uint32(t), Parallelism: uint8(par)}, salt, key, nil //nolint:gosec // bounded above
}

// number reads prefix followed by decimal digits only, from 1 to max.
func number(s, prefix string, max uint64) (uint64, bool) {
	d, ok := strings.CutPrefix(s, prefix)
	if !ok || d == "" || len(d) > 10 {
		return 0, false
	}
	for _, c := range d {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(d, 10, 64)
	if err != nil || n < 1 || n > max {
		return 0, false
	}
	return n, true
}

// alphabetOnly reports whether s holds only characters of the standard base64 alphabet: the
// decoder itself skips line breaks.
func alphabetOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '+' && c != '/' {
			return false
		}
	}
	return true
}
