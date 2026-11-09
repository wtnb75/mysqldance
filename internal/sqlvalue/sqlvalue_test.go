package sqlvalue

import "testing"

func TestLiteral(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		raw      []byte
		isNull   bool
		want     string
	}{
		{"int", "INT", []byte("42"), false, "42"},
		{"unsigned bigint", "UNSIGNED BIGINT", []byte("18446744073709551615"), false, "18446744073709551615"},
		{"lowercase int", "int", []byte("7"), false, "7"},
		{"null", "VARCHAR", nil, true, "NULL"},
		{"escape", "VARCHAR", []byte("a'b\\c\n\r\"\x00\x1a"), false, "'a\\'b\\\\c\\n\\r\\\"\\0\\Z'"},
		{"blob", "BLOB", []byte{0xDE, 0xAD}, false, "0xDEAD"},
		{"longblob", "LONGBLOB", []byte{0x0a, 0xff}, false, "0x0AFF"},
		{"empty blob", "BLOB", []byte{}, false, "''"},
		{"varbinary", "VARBINARY", []byte{0x01}, false, "0x01"},
		{"geometry", "GEOMETRY", []byte{0xab}, false, "0xAB"},
		{"bit", "BIT", []byte{0x05}, false, "b'101'"},
		{"bit multi byte", "BIT", []byte{0x01, 0x00}, false, "b'100000000'"},
		{"bit zero", "BIT", []byte{0x00, 0x00}, false, "b'0'"},
		{"bit empty", "BIT", []byte{}, false, "b'0'"},
		{"decimal", "DECIMAL", []byte("1.50"), false, "1.50"},
		{"year", "YEAR", []byte("2024"), false, "2024"},
		{"invalid utf8", "VARCHAR", []byte{0xFF, 0xFE}, false, "'\xff\xfe'"},
		{"empty string", "VARCHAR", []byte{}, false, "''"},
		{"datetime", "DATETIME", []byte("2024-01-02 03:04:05"), false, "'2024-01-02 03:04:05'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Literal(tt.typeName, tt.raw, tt.isNull)
			if got != tt.want {
				t.Errorf("Literal(%q, %q, %v) = %q, want %q", tt.typeName, tt.raw, tt.isNull, got, tt.want)
			}
		})
	}
}
