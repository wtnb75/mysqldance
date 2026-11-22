package dump

import (
	"context"
	"fmt"

	"mysqldance/internal/mask"
	"mysqldance/internal/source"
)

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
