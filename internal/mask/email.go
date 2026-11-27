package mask

import (
	"bytes"
	"errors"
	"fmt"

	"mysqldance/internal/sqlvalue"
)

// defaultEmailHost is the host hash_email writes when the rule gives none. The
// .invalid top-level domain is reserved (RFC 2606) and never resolves, so a mail
// to a masked address reaches nobody.
const defaultEmailHost = "example.invalid"

// maxEmailHost is the longest host name of DNS, in bytes.
const maxEmailHost = 253

// hashEmailTransform replaces an e-mail address with LOCAL@host: the host is the
// one the rule names (a reserved domain by default) and LOCAL is lower-case
// letters and digits chosen by HMAC(key, value), as many bytes as it takes to keep
// the length of the whole value in bytes. The same address gives the same output
// in every column and table. NULL and the empty string stay as they are.
//
// BreaksKeys and UniqueChecked as hash_keep_shape: two addresses can give one
// output (a short local part, or an address of another domain with the same
// local part length), so the dump compares the rows of a unique index after the
// transform. The key is the one of the transform alone: a domain (B-170) is
// refused, because the output must not depend on where the column is.
var hashEmailTransform = Transform{
	Name:          "hash_email",
	Args:          []ArgDef{{Name: "host", Kind: ArgString}},
	Types:         shapeTypes,
	Version:       1,
	NeedsSalt:     true,
	BreaksKeys:    true,
	UniqueChecked: true,
	NoDomain:      true,
	Build:         buildHashEmail,
}

// emailAlphabet is where the characters of the new local part come from.
const emailAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func buildHashEmail(c BuildContext) (Func, error) {
	if len(c.Key) == 0 {
		return nil, fmt.Errorf("transform %q needs a salt", "hash_email")
	}
	host := defaultEmailHost
	if v, ok := c.Args["host"]; ok {
		s, isString := v.(string)
		if !isString {
			return nil, errors.New("host must be a string")
		}
		host = s
	}
	if err := checkEmailHost(host); err != nil {
		return nil, err
	}
	key := c.Key
	pool := newShapePool(key)
	colType := c.Column.ColumnType
	return func(in Input) (sqlvalue.Value, error) {
		if in.Value.IsNull() {
			return in.Value, nil
		}
		raw := in.Value.Bytes()
		if len(raw) == 0 {
			return in.Value, nil
		}
		if bytes.LastIndexByte(raw, '@') < 0 {
			return sqlvalue.Value{}, fmt.Errorf("a value of type %s is not an e-mail address (it has no @)", colType)
		}
		// The new local part takes the bytes of the old address that the host does
		// not: len(raw) = n + 1 + len(host).
		n := len(raw) - 1 - len(host)
		// A short address is an error by the spec (B-185, R-242): neither the length
		// nor the host may change, so the user picks a shorter host for the column.
		if n < 1 {
			return sqlvalue.Value{}, fmt.Errorf("a value of type %s is too short to hold an address at %q", colType, host)
		}
		out := make([]byte, 0, len(raw))
		st := pool.Get().(*shapeState)
		st.start()
		for range n {
			// 65536 mod 36 = 16: the bias is about 0.05%, the same method as hash_keep_shape (R-243).
			out = append(out, emailAlphabet[st.number(raw, 2)%uint64(len(emailAlphabet))])
		}
		pool.Put(st)
		out = append(out, '@')
		out = append(out, host...)
		return sqlvalue.NewValue(in.Value.Type(), out, false), nil
	}, nil
}

// checkEmailHost checks the format of a host: 1 to 253 bytes of ASCII letters,
// digits, "." and "-". It has no "@", so the last "@" of an output is the one
// before it.
func checkEmailHost(h string) error {
	if h == "" || len(h) > maxEmailHost {
		return fmt.Errorf("host must be 1 to %d bytes long (got %d)", maxEmailHost, len(h))
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' {
			continue
		}
		return errors.New("host may hold only ASCII letters, digits, \".\" and \"-\"")
	}
	return nil
}
