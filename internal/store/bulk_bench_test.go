package store

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkBrowseDuringBulk measures what a person browsing sees while a
// library-sized write runs: ListItems on a 3,000-item library (two readers, back
// to back) and a progress write every 20 ms, both timed for as long as a 3,000-row
// bulk write is in flight. It uses the public API only, so it runs on any version
// of the store. Run it with -benchtime=1x:
//
//	go test ./internal/store -run '^$' -bench BrowseDuringBulk -benchtime=1x
//
// "bulk-delete" is DeleteItems of 3,000 rows, five rounds; "upserts" is 3,000
// UpsertItem calls from 4 workers, like a scan. The metrics are over all rounds.
func BenchmarkBrowseDuringBulk(b *testing.B) {
	for _, mode := range []string{"bulk-delete", "upserts"} {
		b.Run(mode, func(b *testing.B) {
			for range b.N {
				browseDuringBulk(b, mode)
			}
		})
	}
}

// BenchmarkListItemsReaders is the same browse with nothing else going on, by
// the number of readers at once: what a read costs when it has the database to
// itself, and how much of it reads in parallel can take back.
func BenchmarkListItemsReaders(b *testing.B) {
	ctx := context.Background()
	s, err := Open(filepath.Join(b.TempDir(), "ply.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	for i := range 3000 {
		if _, err := s.UpsertItem(ctx, movie(fmt.Sprintf("/media/keep/%05d.mp4", i), fmt.Sprintf("Keep %05d", i)), videoAudio); err != nil {
			b.Fatal(err)
		}
	}
	for _, readers := range []int{1, 2, 4} {
		b.Run(fmt.Sprintf("readers=%d", readers), func(b *testing.B) {
			var (
				wg sync.WaitGroup
				mu sync.Mutex
				ds []time.Duration
			)
			for range readers {
				wg.Go(func() {
					var mine []time.Duration
					for range max(b.N, 10) {
						t := time.Now()
						if _, err := s.ListItems(ctx, ListFilter{Kind: KindMovie}); err != nil {
							b.Error(err)
							return
						}
						mine = append(mine, time.Since(t))
					}
					mu.Lock()
					ds = append(ds, mine...)
					mu.Unlock()
				})
			}
			wg.Wait()
			slices.Sort(ds)
			b.ReportMetric(float64(ds[len(ds)/2])/float64(time.Millisecond), "list_p50_ms")
		})
	}
}

func browseDuringBulk(b *testing.B, mode string) {
	const keep, victims = 3000, 3000
	rounds := 5
	if mode == "upserts" {
		rounds = 1
	}
	ctx := context.Background()
	s, err := Open(filepath.Join(b.TempDir(), "ply.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	stream := videoAudio[:1]
	var probeID int64
	for i := range keep {
		id, err := s.UpsertItem(ctx, movie(fmt.Sprintf("/media/keep/%05d.mp4", i), fmt.Sprintf("Keep %05d", i)), videoAudio)
		if err != nil {
			b.Fatal(err)
		}
		probeID = id
	}

	var (
		lists, progress []time.Duration
		bulk            time.Duration
	)
	for round := range rounds {
		var victimIDs []int64
		if mode == "bulk-delete" {
			for i := range victims {
				id, err := s.UpsertItem(ctx, episode(fmt.Sprintf("/media/gone/%d/%05d.mkv", round, i), "Gone", 1, i), stream)
				if err != nil {
					b.Fatal(err)
				}
				victimIDs = append(victimIDs, id)
			}
		}

		var (
			stop             = make(chan struct{})
			readers, writers sync.WaitGroup
			mu               sync.Mutex
			listed           atomic.Int64
		)
		for range 2 {
			readers.Go(func() {
				var mine []time.Duration
				defer func() {
					mu.Lock()
					lists = append(lists, mine...)
					mu.Unlock()
				}()
				for {
					select {
					case <-stop:
						return
					default:
					}
					t := time.Now()
					es, err := s.ListItems(ctx, ListFilter{Kind: KindMovie})
					d := time.Since(t)
					if err != nil {
						b.Error(err)
						return
					}
					listed.Store(int64(len(es)))
					mine = append(mine, d)
				}
			})
		}
		writers.Go(func() {
			var mine []time.Duration
			defer func() {
				mu.Lock()
				progress = append(progress, mine...)
				mu.Unlock()
			}()
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for pos := 100.0; ; pos++ {
				select {
				case <-stop:
					return
				case <-tick.C:
				}
				t := time.Now()
				if _, err := s.SetProgress(ctx, probeID, pos, 7200); err != nil {
					b.Error(err)
					return
				}
				mine = append(mine, time.Since(t))
			}
		})

		time.Sleep(100 * time.Millisecond) // readers and the progress writer are going
		start := time.Now()
		switch mode {
		case "bulk-delete":
			if err := s.DeleteItems(ctx, victimIDs); err != nil {
				b.Fatal(err)
			}
		case "upserts":
			var next atomic.Int64
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					for {
						i := int(next.Add(1)) - 1
						if i >= victims {
							return
						}
						if _, err := s.UpsertItem(ctx, episode(fmt.Sprintf("/media/new/%05d.mkv", i), "New", 1, i), stream); err != nil {
							b.Error(err)
							return
						}
					}
				})
			}
			wg.Wait()
		}
		bulk += time.Since(start)
		time.Sleep(50 * time.Millisecond)
		close(stop)
		readers.Wait()
		writers.Wait()
		if listed.Load() != keep {
			b.Fatalf("ListItems returned %d items, want %d", listed.Load(), keep)
		}
	}

	pct := func(ds []time.Duration, p float64) float64 {
		if len(ds) == 0 {
			return 0
		}
		slices.Sort(ds)
		return float64(ds[min(len(ds)-1, int(p*float64(len(ds))))]) / float64(time.Millisecond)
	}
	b.ReportMetric(float64(bulk)/float64(time.Millisecond)/float64(rounds), "bulk_ms")
	b.ReportMetric(float64(len(lists)), "list_n")
	b.ReportMetric(pct(lists, 0.50), "list_p50_ms")
	b.ReportMetric(pct(lists, 0.99), "list_p99_ms")
	b.ReportMetric(pct(lists, 1), "list_max_ms")
	b.ReportMetric(float64(len(progress)), "prog_n")
	b.ReportMetric(pct(progress, 0.50), "prog_p50_ms")
	b.ReportMetric(pct(progress, 0.99), "prog_p99_ms")
	b.ReportMetric(pct(progress, 1), "prog_max_ms")
}
