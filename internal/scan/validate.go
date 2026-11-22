package scan

import (
	"slices"
	"sort"
)

// validators are the checks a rule can name with its validate key. A check is
// given the secret the regex found and says whether it counts as a detection.
// It is a pure function of the secret, and it never sees more than the secret.
var validators = map[string]func(secret string) bool{
	"luhn":      luhn,
	"my_number": myNumber,
}

func validatorNames() []string {
	names := make([]string, 0, len(validators))
	for n := range validators {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// digitsOnly returns the digits of s with the separators dropped (a space, and a
// hyphen when hyphen is set), and false if s holds anything else. A card number
// may be written with hyphens; a my-number only with spaces.
func digitsOnly(s string, hyphen bool) ([]int, bool) {
	out := make([]int, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			out = append(out, int(c-'0'))
		case c == ' ' || (hyphen && c == '-'):
		default:
			return nil, false
		}
	}
	return out, true
}

// luhn reports whether the digits of s pass the Luhn check (the sum of the
// digits, every second one from the right doubled, is a multiple of 10).
func luhn(s string) bool {
	d, ok := digitsOnly(s, true)
	if !ok || len(d) < 2 {
		return false
	}
	sum := 0
	for i, n := range slices.Backward(d) {
		if (len(d)-1-i)%2 == 1 {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
	}
	return sum%10 == 0
}

// myNumber reports whether s is a Japanese Individual Number: 12 digits (with
// spaces allowed) whose last digit is the check digit of the first 11. Counting
// the 11 digits from the right, the n-th is multiplied by n+1 for n up to 6 and
// by n-5 for n from 7; the sum modulo 11 is r, and the check digit is 0 when r
// is 0 or 1 and 11-r otherwise.
func myNumber(s string) bool {
	d, ok := digitsOnly(s, false)
	if !ok || len(d) != 12 {
		return false
	}
	sum := 0
	for n := 1; n <= 11; n++ {
		p := d[11-n]
		q := n + 1
		if n >= 7 {
			q = n - 5
		}
		sum += p * q
	}
	check := 0
	if r := sum % 11; r > 1 {
		check = 11 - r
	}
	return d[11] == check
}
