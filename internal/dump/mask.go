package dump

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"mysqldance/internal/mask"
	"mysqldance/internal/source"
)

// ErrUnusedRules is what Run returns, once the dump is complete and in place,
// when StrictRules is set and a masking rule did nothing on a table that has
// rows. The output is whole: only the rules that did something masked anything.
var ErrUnusedRules = errors.New("a masking rule matched no row")

// reportMaskUsage logs, for each rule that calls a transform, how many rows it
// was used on (Info), and a rule that did nothing on a table with rows as a
// warning: a misspelled path looks like that (a misspelled column is already an
// error when the rules are checked), and so does a column that is all NULL. It never logs a value.
// With strict, it returns ErrUnusedRules naming those rules. Run calls it only
// for a dump that is written, so a failed dump is not judged.
func reportMaskUsage(log *slog.Logger, plan *mask.Plan, strict bool) error {
	var unused []string
	for _, u := range plan.Usage() {
		rule := u.DB + "." + u.Table + "." + u.Column
		attrs := []any{"rule", rule}
		if u.Path != "" {
			attrs = append(attrs, "path", u.Path)
		}
		attrs = append(attrs, "use", u.Use, "rows_hit", u.Hits, "table_rows", u.Rows)
		if !u.Unused() {
			log.Info("mask rule used", attrs...)
			continue
		}
		log.Warn("mask rule matched no row: its column is NULL in every row, or its path is in no document (check the path in the rules file)", attrs...)
		if u.Path != "" {
			rule += " " + u.Path
		}
		unused = append(unused, rule)
	}
	if strict && len(unused) > 0 {
		return fmt.Errorf("--strict-rules: %w: %d rule(s) found no value in any row (the dump is complete and in place): %s",
			ErrUnusedRules, len(unused), strings.Join(unused, ", "))
	}
	return nil
}

// resolveMask checks the masking rules of opt against the catalog of every
// database to dump, and returns the plan that transforms the rows. It reads the
// catalog only. The connection is left on the first database, where Run
// expects it.
func resolveMask(ctx context.Context, opt Options, snap *source.Snapshot) (*mask.Plan, error) {
	cat, err := snap.MaskCatalog(ctx, opt.DBs)
	if err != nil {
		return nil, err
	}
	plan, err := opt.Mask.Resolve(cat, mask.DefaultRegistry(), opt.MaskSalt)
	if err != nil {
		return nil, fmt.Errorf("mask rules: %w", err)
	}
	if opt.StrictColumns {
		if err := opt.Mask.CheckStrictColumns(cat); err != nil {
			return nil, fmt.Errorf("mask rules: %w", err)
		}
	}
	return plan, nil
}
