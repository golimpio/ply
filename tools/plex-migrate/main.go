// Command plex-migrate moves a Plex library into ply: it joins the two by exact
// file path, writes Plex's chosen artwork as sidecars next to the media (which
// ply serves over TMDB and pins), imports watch state, and matches titles by
// their IMDb/TMDB ids. Plex is only ever read.
//
// Default is a dry run (prints the plan). Add -apply to write. Examples:
//
//	# DB mode (Plex stopped or remote folder mounted), dry run:
//	plex-migrate -plex-data "/path/to/Plex Media Server" -ply-data ~/Library/Application\ Support/ply
//
//	# API mode, apply, with a mount-prefix mapping:
//	PLEX_TOKEN=… PLY_TOKEN=… plex-migrate -plex-url http://nas:32400 \
//	  -ply-data /var/lib/ply -ply-url http://nas:8420 \
//	  -path-map /volume1/Movies=/mnt/Movies -apply
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"ply/tools/plex-migrate/migrate"
	"ply/tools/plex-migrate/plex"
)

type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "plex-migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		plexData  = flag.String("plex-data", "", "Plex data directory (DB mode: reads a copy of the database + bundles)")
		plexURL   = flag.String("plex-url", "", "Plex server URL (API mode), e.g. http://host:32400")
		plexToken = flag.String("plex-token", os.Getenv("PLEX_TOKEN"), "Plex X-Plex-Token (or env PLEX_TOKEN; never logged)")
		account   = flag.Int64("account", 1, "Plex account id whose watch state to read (DB mode)")
		plyData   = flag.String("ply-data", "", "ply data directory (reads ply.db read-only for the path join)")
		plyURL    = flag.String("ply-url", "", "ply server URL for matching/watch writes, e.g. http://host:8420")
		plyToken  = flag.String("ply-token", os.Getenv("PLY_TOKEN"), "ply API token (or env PLY_TOKEN)")
		apply     = flag.Bool("apply", false, "actually write (default: dry run, prints the plan only)")
		force     = flag.Bool("force", false, "overwrite existing sidecars")
		maps      stringSlice
	)
	flag.Var(&maps, "path-map", "rewrite Plex paths to ply paths, old=new (repeatable)")
	flag.Parse()

	if *plyData == "" {
		return fmt.Errorf("-ply-data is required (the ply data dir, for the path join)")
	}
	var src plex.Source
	switch {
	case *plexData != "" && *plexURL != "":
		return fmt.Errorf("give either -plex-data (DB mode) or -plex-url (API mode), not both")
	case *plexData != "":
		s, err := plex.OpenDB(context.Background(), *plexData, *account)
		if err != nil {
			return err
		}
		src = s
	case *plexURL != "":
		if *plexToken == "" {
			return fmt.Errorf("API mode needs -plex-token (or PLEX_TOKEN)")
		}
		src = plex.NewAPI(*plexURL, *plexToken)
	default:
		return fmt.Errorf("give -plex-data (DB mode) or -plex-url + -plex-token (API mode)")
	}
	defer src.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pm, err := migrate.ParsePathMap(maps)
	if err != nil {
		return err
	}
	plyItems, err := migrate.ReadPlyItems(ctx, *plyData)
	if err != nil {
		return err
	}
	plan, err := migrate.BuildPlan(ctx, src, plyItems, pm, *force)
	if err != nil {
		return err
	}
	fmt.Print(plan.Render())
	if !*apply {
		fmt.Println("\nDry run. Re-run with -apply (and -ply-url/-ply-token) to write.")
		return nil
	}
	if *plyURL == "" {
		return fmt.Errorf("-apply needs -ply-url (and -ply-token) for matching/watch writes")
	}
	client := migrate.NewPlyClient(*plyURL, *plyToken)
	fmt.Println("\nApplying…")
	if err := migrate.Apply(ctx, plan, src, client, *force, func(s string) { fmt.Println(s) }); err != nil {
		return err
	}
	fmt.Println("Done.")
	return nil
}
