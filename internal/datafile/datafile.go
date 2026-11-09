// Package datafile writes the per-table data file: a fixed header followed by
// INSERT statements with one row per line, chunked by row content.
package datafile

import (
	"bufio"
	"hash/fnv"
	"io"
	"strings"
)

const (
	defaultRowsPerInsert  = 1000
	defaultMaxInsertBytes = 1048576
)

// Options controls how rows are grouped into INSERT statements.
type Options struct {
	// RowsPerInsert is the average number of rows per statement (default 1000).
	RowsPerInsert uint32
	// MaxInsertBytes is a safety cap on statement size (default 1048576).
	MaxInsertBytes int
}

func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// Header returns the fixed preamble of every dump file for the given database.
func Header(db string) string {
	return "SET NAMES utf8mb4;\n" +
		"SET TIME_ZONE='+00:00';\n" +
		"SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO';\n" +
		"SET FOREIGN_KEY_CHECKS=0;\n" +
		"SET UNIQUE_CHECKS=0;\n" +
		"USE " + quoteIdent(db) + ";\n"
}

// Write writes the header and the rows of one table as INSERT statements.
//
// Statement boundaries are content-defined: a statement is closed after a row
// whose FNV-1a 32-bit hash modulo RowsPerInsert is zero, after the row that
// makes the statement reach MaxInsertBytes, and after the last row. Removing
// one row therefore only affects boundaries next to it.
func Write(w io.Writer, db, table string, columns []string, listColumns bool, rows [][]string, opt Options) error {
	if opt.RowsPerInsert == 0 {
		opt.RowsPerInsert = defaultRowsPerInsert
	}
	if opt.MaxInsertBytes <= 0 {
		opt.MaxInsertBytes = defaultMaxInsertBytes
	}

	bw := bufio.NewWriter(w)
	if _, err := bw.WriteString(Header(db)); err != nil {
		return err
	}

	var head strings.Builder
	head.WriteString("INSERT INTO " + quoteIdent(table))
	if listColumns {
		quoted := make([]string, len(columns))
		for i, c := range columns {
			quoted[i] = quoteIdent(c)
		}
		head.WriteString(" (" + strings.Join(quoted, ", ") + ")")
	}
	head.WriteString(" VALUES\n")

	open := false
	size := 0
	for i, row := range rows {
		if !open {
			if _, err := bw.WriteString(head.String()); err != nil {
				return err
			}
			size = head.Len()
			open = true
		}
		text := "(" + strings.Join(row, ",") + ")"
		h := fnv.New32a()
		_, _ = h.Write([]byte(text))
		size += len(text) + 2
		closeNow := i == len(rows)-1 || h.Sum32()%opt.RowsPerInsert == 0 || size >= opt.MaxInsertBytes
		sep := ",\n"
		if closeNow {
			sep = ";\n"
		}
		if _, err := bw.WriteString(text + sep); err != nil {
			return err
		}
		if closeNow {
			open = false
		}
	}
	return bw.Flush()
}
