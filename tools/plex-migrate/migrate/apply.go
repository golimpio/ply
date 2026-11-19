package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"ply/tools/plex-migrate/plex"
)

// Apply executes the plan's non-skipped ops. Artwork is fetched from the Plex
// source and written next to the media via temp+rename; matches and watch state
// go through the ply API. It is idempotent: an existing sidecar is left alone
// unless force, and a Plex item with no image writes nothing. Plex is only read.
func Apply(ctx context.Context, p *Plan, src plex.Source, ply *PlyClient, force bool, log func(string)) error {
	var errs []error
	for _, op := range p.Ops {
		if op.Skip {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var err error
		switch op.Type {
		case OpPoster, OpBackdrop, OpSeasonPoster, OpEpisodeStill:
			err = applyArt(ctx, op, src, force)
		case OpMatchMovie:
			err = ply.MatchMovie(ctx, op.PlyID, op.TMDB, op.IMDb)
		case OpMatchShow:
			err = ply.MatchShow(ctx, op.PlyShow, op.TMDB, op.IMDb)
		case OpProgress:
			err = ply.SetProgress(ctx, op.PlyID, op.PositionSec, op.DurationSec)
		case OpWatched:
			err = ply.SetWatched(ctx, op.PlyID, op.Watched)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", op.Type, opDesc(op), err))
			if log != nil {
				log(fmt.Sprintf("FAIL %s %s: %v", op.Type, opDesc(op), err))
			}
		} else if log != nil {
			log(fmt.Sprintf("ok   %s %s", op.Type, opDesc(op)))
		}
	}
	return errors.Join(errs...)
}

func opDesc(op Op) string {
	if op.Target != "" {
		return op.Target
	}
	if op.PlyShow != "" {
		return op.PlyShow
	}
	return fmt.Sprintf("ply item %d", op.PlyID)
}

func applyArt(ctx context.Context, op Op, src plex.Source, force bool) error {
	if !force {
		if _, err := os.Stat(op.Target); err == nil {
			return nil // a sidecar appeared since the plan; leave it
		}
	}
	b, err := src.Art(ctx, op.Plex, op.Slot)
	if err != nil {
		if errors.Is(err, plex.ErrNoArt) {
			return nil // nothing to migrate for this slot
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return writeFileAtomic(op.Target, b)
}

// writeFileAtomic writes b to target through a temp file in the same directory,
// renamed into place, so a half-written poster is never served.
func writeFileAtomic(target string, b []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".migrate-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}
