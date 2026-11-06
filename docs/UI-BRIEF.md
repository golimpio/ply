# ply UI brief: design sprint, step 1

> **Status (2026-11-06): the three directions below were rejected.** The owner judged their prototypes "a step down
> from what we have today". The current UI is evolved instead (see DESIGN.md › Styles and UI-REDESIGN-PLAN.md).
> What remains useful: the audience and jobs, the constraints, and the findings on the current UI (the Home hero
> choosing the last-added episode, episodes flooding Recently added, grey slabs while posters load, logos you can't
> read on their backdrop, hard-coded values). Read the directions only as rejected alternatives.

This brief is for the three prototype designers. It covers who ply is for, what it must do, what the current UI
gets right and wrong, the references, and three candidate directions with their tokens. Read it all before you start.
Each prototype builds **one** direction on the real data snapshot (see "Sprint materials" at the end).

## 1. Subject and audience

ply is one person's film collection, served from a Mac to the family. It holds about 260 films, from 1922 to this year.
Most are world and art-house cinema, with family films and blockbusters alongside, plus about 90 TV shows. The files
live on a NAS, which is slow to read cold. It is not a streaming service. Nothing is for sale, nothing is promoted,
and everything on screen belongs to the people watching.

| Who | Device | Situation |
|---|---|---|
| The owner | Mac browser, 1280–1728 px | Manages the collection: folders, wrong matches, the job queue. Also picks films at the desk. |
| The family, together | TV through AirPlay or Chromecast, picked on a phone or iPad; later a TV app with a remote | On the sofa in the evening: "what are we watching tonight?" Several people, low light, 3 m from the screen. |
| One person, alone | iPhone (360–430 px) or iPad | Resumes a show in bed, or browses the collection in a spare moment. One hand, short sessions. |

## 2. The jobs, ranked

1. **Resume what we were watching.** One action from opening the app. An episode in progress goes before anything else.
2. **Pick something for tonight.** Narrow 260 films to a few good candidates, fast, as a group. The useful facts are
   length (when will it end?), mood or genre, whether anyone has seen it, ratings, and who made it.
3. **Browse the collection.** Enjoy it as a collection: by decade, director, genre or collection. Find a known title in two keystrokes.
4. **Manage it.** Folders, fixing matches, metadata keys, the activity queue and playback limits. This is the owner, on the Mac.
   It must be clear and complete, and it must stay out of the way of jobs 1–3.

## 3. Constraints

- **Local-only.** No web fonts from CDNs, no external scripts, no remote images except through ply's `/api/images/…` proxy.
  Fonts are system stacks or files self-hosted in `web/public/fonts/` (woff2, subset, at most two weights per family,
  `font-display: swap`).
- **Fast on a slow NAS.** Text and layout render before any image. Every image slot has a reserved aspect ratio and a
  colour or typographic placeholder, and the image loads progressively: small size first, `loading="lazy"`,
  `decoding="async"`, and `fetchpriority="high"` only for the one hero image. No layout shift when images arrive.
- **From 360 px to TV.** One codebase, from a 360 px phone to a 1920 px TV browser, with the same routes.
- **Remote-friendly focus.** Everything is reachable with the arrow keys, Enter and Back (Escape or Backspace) alone.
  Focus is always visible, is never lost on a route change, and comes back to where it was on Back.
- **Accessibility.** WCAG 2.2 AA: contrast, 44 px touch targets (56 px on TV), `prefers-reduced-motion`,
  `prefers-contrast`, text zoom to 200 %, and real semantics (landmarks, headings, buttons that are buttons).
- **Svelte 5, the existing routes and API.** Hash routes: `#/`, `#/movies`, `#/shows`, `#/shows/<name>`, `#/item/<id>`,
  `#/play/<id>`, `#/person/<id>`, `#/genre/<id>`, `#/collection/<id>`, `#/settings` and `#/activity`. Use the data the API
  already gives. Any new field a direction wants is listed as an API ask, and the design must work without it.
- **Attributions stay.** These are TMDB's notice and logo, TheTVDB's credit, IMDb's dataset attribution ("Information
  courtesy of IMDb (https://www.imdb.com). Used with permission."), and MDBList's credit with a link wherever its numbers
  appear. Rating chips link to their source. See `docs/M10-RATINGS.md`.

## 4. The current UI

The screenshots are in the sprint scratchpad, under `design/current/` (see "Sprint materials").

**What it gets right**
- **Projection light.** Each page takes a `--tint` from the film's artwork, so the room takes on the film's colour
  (`item-1280.png`: Breathless's red bleeds into the page). It is cheap and specific to film. Keep the idea.
- **Logos as titles.** TMDB logos stand in for the title text (`show-1280.png`, `item-1280.png`). 245 of 263 films have one.
- **Good foundations.** It has `:focus-visible`, `prefers-reduced-motion`, tabular numbers, metadata separated by space
  rather than dots, a card-to-detail view transition, skeletons, and a phone tab bar.
- **Plain copy.** For example "Videos playing at once", and "Off keeps everything on this network" (`settings-1280-full.png`).

**What it gets wrong**
- **The hero is not a choice.** Home leads with whatever was added last that has a backdrop. Today that is a sitcom
  episode still, in front of a collection of Godard and Akerman (`home-1280.png`). "Recently added" is then flooded by
  one show's episodes (`home-1280-rails.png`).
- **The Netflix template.** A hero, then identical horizontal rails: Recently added, Movies (A to Z!), Shows, Coming up,
  then Drama, Comedy and Action. An alphabetical rail is not curation. Nothing answers "what tonight?", and nothing makes
  the collection feel like a collection.
- **The NAS shows up as empty space.** Off-screen rails are blank until scrolled to (`home-1280-full.png`). Episode
  thumbnails and cast portraits sit as grey slabs, and old films have no portraits at all (`show-1280-full.png`,
  `item-1280-full.png`).
- **Artwork collisions.** Breathless's black logo sits on a dark scrim (`item-1280.png`). Silo's backdrop has its own
  tagline and a streamer's logo baked in, which fight the title (`show-1280.png`).
- **The viewer's page carries the technician's facts.** "In a web browser: Remux (HLS), audio is re-encoded" and source
  badges ("TMDB and .nfo") sit beside the synopsis and title (`item-1280-full.png`, `show-1280.png`).
- **Walls of underlined links.** Silo lists 10 directors and 13 writers, every one underlined (`show-1280-full.png`).
- **Typography does nothing.** One `system-ui` family at weight 800 for headings and 16 px for the rest, and the
  wordmark is bold body text. There is no voice.
- **Chrome.** The header has five controls on the right, one of them Rescan, a maintenance action given top-level
  prominence. On the phone, Settings appears twice (the header gear and the tab bar), and the tab bar has five items
  (`home-390.png`).
- **Phone density.** Movies shows two posters per screen, and the sort and filter chips take three rows
  (`movies-390.png`). That is about 130 screens of scrolling for 263 films.
- **Settings.** A single 600 px column on a 1280 px screen, mixed button styles, and no section navigation
  (`settings-1280-full.png`).
- **Tokens exist but aren't used.** The Svelte components hold 68 raw colours across 21 files, 89 raw `font-size`
  values (none uses a token) and 40 raw radii. Changing the theme today means editing every component.

## 5. Reference board

| Reference | Take | Avoid |
|---|---|---|
| [Apple TV app, tvOS 26](https://www.apple.com/newsroom/2025/06/apple-tv-brings-a-beautiful-redesign-and-enhanced-home-entertainment-experience/) | Poster-first browsing that fits more titles on screen; focus as a physical lift; the player's controls shrink to almost nothing. | Liquid Glass. It is [GPU-heavy enough that Apple dropped it on older hardware](https://appleinsider.com/articles/25/06/09/liquid-glass-tvos-redesign-is-restricted-to-second-generation-apple-tv-4k-and-later), and `backdrop-filter` on large areas is the costliest thing a TV browser can paint. |
| [Infuse 8](https://firecore.com/blog/infuse-8-now-available) | A local library treated as first class; fast browsing after a ground-up rewrite; an alphabet scrubber for long lists. | Posters forced onto everything: users [object to 2:3 frames on 16:9 files](https://community.firecore.com/t/browsing-folders-should-not-use-posters/59254), and to busy, coloured rating icons. |
| [MUBI](https://mubi.com) | Editorial voice: each film with a reason to watch it; strong typographic identity; few titles at a time. | A catalogue that is [hard to browse](https://kimola.com/reports/unveil-the-truth-mubi-app-customer-feedback-analysis-app-store-us-141803) because curation replaces search. Our viewers know their collection. |
| [Criterion Channel](https://www.criterionchannel.com) | Collections by director, decade and movement; original poster art rather than auto-generated stills; restraint ([commentary](https://desirabilitylab.com/backfill/backfill-2024-339-criterion-channel-app)). | Black-and-white austerity that reads as cold on a family TV. |
| [Letterboxd](https://letterboxd.com) | The poster grid as a collector's wall; crew as a first-class way in ("films by Godard"); ratings shown small and exact. | Social clutter: reviews, likes, follower counts. *(From prior knowledge, not this sprint's search.)* |
| [Plex, "New Experience"](https://forums.plex.tv/t/new-experience-public-release-feedback/910904) | A cautionary tale. | Personal media pushed below promoted content, sections buried under menus, and [a rollback after the backlash](https://www.aftvnews.com/tag/plex). ply's library is the whole product. |
| [Jellyfin](https://jellyfin.org) | Open, complete library management, which ply should match. | Its web UI is a Netflix clone of hero plus rails, which is what ply has now. Settings that are a maze. *(Prior knowledge.)* |
| [Kodi](https://kodi.tv) | Built for the keyboard and remote from day one, and skinnable: the whole look is a swappable layer, which our tokens file copies. | Settings depth, and skins that show every fact at once. *(Prior knowledge.)* |
| [Smashing, "Designing for TV" (2025)](https://smashingmagazine.com/2025/09/designing-tv-principles-patterns-practical-guidance/), [Apple HIG for tvOS](https://developer.apple.com/design/human-interface-guidelines/designing-for-tvos) | Grids and lines for the D-pad; focus that is visible at 3 m; a 5–6 step type scale; a 5 % overscan-safe margin; nothing that needs careful reading. | Lean-forward density on a lean-back screen. |

## 6. Implementation approach (all directions)

*Decided, pending the owner's OK.*

- **No styled UI kit** (Skeleton, shadcn-svelte, Flowbite and the like). They pull the look toward their defaults,
  against the owner's wish for something distinctive, and most need Tailwind.
- **No utility-framework migration** (Tailwind, UnoCSS) for now. A token layer plus our own primitives meets the DRY and
  replaceable goals without rewriting about 3,300 lines of scoped CSS into class strings.
- **Our own token layer, `web/src/styles/tokens.css`,** in three tiers:
  1. **Primitive tokens:** the palette, a spacing scale, radii, shadows and motion.
  2. **Semantic tokens** built on them: `--surface-1`, `--text-muted`, `--accent`, `--focus-ring`, and the rest.
  3. **Type tokens:** `--font-display`, `--font-text`, `--font-mono`, and a fluid scale `--step--1 … --step-6` with
     `clamp()`, plus line-heights and weights.

  Each direction is a different tokens file over the same components.
- **A small set of our own Svelte primitives,** styled only from tokens: Button, IconButton, Chip, Card, Field (input
  and select), Switch, SegmentedControl, Dialog, Menu, Tooltip, Toast and Skeleton.
- **[Bits UI](https://bits-ui.com)** (headless, unstyled, WAI-ARIA) for the hard accessible behaviours: Select,
  Combobox, Popover, Tooltip, DropdownMenu and Dialog. Our primitives wrap it, so the look stays ours.
- **Enforcement:** a Stylelint rule (for example `stylelint-declaration-strict-value`) that bans raw colours, font
  sizes and font families outside `tokens.css`, run in `make check`.
- **Migration:** `tokens.css` keeps the old names (`--bg`, `--bg-2`, `--bg-3`, `--line`, `--text`, `--muted`,
  `--accent`, `--danger`, `--ok`, `--warn`, `--radius`) as aliases of the semantic tokens, because `Player.svelte` and
  other components use them. They are removed once nothing reads them.

### The token contract

Every direction defines **exactly these semantic names**. Components use only these, never a primitive, so a theme swap is
a file swap. A direction may add primitives freely, but no new semantic name without adding it to all three.

```
surfaces   --surface-0 (page)  --surface-1 (raised)  --surface-2 (control)  --scrim (over artwork)
text       --text  --text-muted  --text-on-accent  --shadow-text
lines      --line  --line-strong
colour     --accent  --tint (set at runtime from artwork; defaults to --accent)  --ok  --warn  --danger
focus      --focus-ring  --focus-width  --focus-offset  --focus-halo
space      --space-1 … --space-9  --gutter
shape      --radius-s  --radius-m  --radius-l  --radius-pill  --shadow-1  --shadow-2
motion     --dur-fast  --dur-base  --dur-slow  --dur-scene  --ease-out  --ease-in-out
layers     --z-raised  --z-header  --z-overlay  --z-dialog  --z-toast
type       --font-display  --font-text  --font-mono  --weight-text  --weight-strong  --weight-display
           --tracking-display  --leading-tight  --leading-snug  --leading-body  --measure
           --step--1 … --step-6
sizing     --target-min  --card-w (rail card width)
```

**Structural primitives, identical in all three.** Each tokens file starts with this block:

```css
:root {
  --space-1: 0.25rem; --space-2: 0.5rem; --space-3: 0.75rem; --space-4: 1rem; --space-5: 1.5rem;
  --space-6: 2rem; --space-7: 3rem; --space-8: 4rem; --space-9: 6rem;
  --gutter: clamp(var(--space-4), 5vw, var(--space-9)); /* 5vw = the TV overscan-safe margin */
  --z-raised: 1; --z-header: 20; --z-overlay: 30; --z-dialog: 40; --z-toast: 50;
  --focus-offset: 3px;
  --radius-pill: 999px;
  --measure: 65ch;
}
/* Set by JS when arrows are used with no pointer: the remote "mode" (bigger targets, stronger focus). */
:root[data-input='remote'] { --target-min: 56px; }
```

## 7. Three directions

Each has one signature idea. Spend boldness there, and keep everything else quiet. In the wireframes, the IMDb
figures are real; the Rotten Tomatoes and Letterboxd figures are illustrative, because MDBList isn't set up yet.

---

### A. Repertory: the house programme

**Concept.** ply as the printed programme of a small repertory cinema whose only audience is this family. Type comes
first. Every film can be shown fully **without its image**: a title card set in condensed type, with year, director
and running time. On a slow NAS, the grid is complete the instant the JSON arrives, and posters fade in behind the type
as they load. Grey boxes are gone, because the placeholder *is* the design. The library reads as a season of
screenings: by decade, by director, by collection.

**Signature: the evening clock.** Every Play button says when the film would end: "Play, ends 22:14". Home opens on
*Tonight*: three unwatched films that fit before a bedtime the viewer sets (default 23:00). They are picked by a
date-seeded rule, so every device in the house shows the same three that evening. This uses only `durationSec`,
`progress` and `genres`, all in the existing API. It answers the sofa question with the fact the sofa actually argues
about.

**Palette.** The card stock and ink of a printed programme, in the dark.

| Name | Hex | Role |
|---|---|---|
| Bottle | `#0E2420` | page (deep bottle-green card stock) |
| Booth | `#15302B` | raised surfaces |
| Newsprint | `#EEE8DA` | text (13.3:1 on Bottle) |
| Faded | `#A8B8AE` | muted text (7.8:1) |
| Ticket | `#F4A6B7` | accent: a pink ticket stub (8.5:1); focus and the evening clock |
| Ticket ink | `#3A0F1A` | text on Ticket |

**Type.**
- **Display:** *Avenir Next Condensed*, which ships with macOS and iOS. Self-host *Barlow Condensed* (SIL OFL) as the
  fallback for Android, Windows and Chromecast. It is used for titles, nav and buttons.
- **Text:** *Iowan Old Style* (macOS and iOS), falling back to self-hosted *Source Serif 4* (OFL). It is used for
  programme notes and synopses, at a measure of 62ch and leading 1.6.
- **Mono:** only in the file-details table.

**Layout.** Left-aligned throughout, ragged right. The structure comes from size and space, not from rules or boxes.

```
HOME (1280)
 ply    Tonight   Library   Shows                                  Search  ⌘K
 
 Tonight                                            in bed by  [ 23:00 ▾ ]
 Three that fit before eleven
 
 ┌───────────────┐  ┌───────────────┐  ┌───────────────┐
 │ Breathless    │  │ Chungking     │  │ Flee          │   title cards: type first,
 │               │  │ Express       │  │               │   the poster fades in
 │ 1960   1h 30m │  │ 1994   1h 43m │  │ 2021   1h 29m │   behind it
 │ Godard        │  │ Wong Kar-wai  │  │ Rasmussen     │
 │ ends 22:11    │  │ ends 22:24    │  │ ends 22:10    │
 └───────────────┘  └───────────────┘  └───────────────┘
 
 Where you left off   Silo  season 2, episode 4   32 min left   [ Resume, ends 21:13 ]
 
 New in the collection        films only; a show's new episodes fold into one line
 By decade   1920s  1940s  1950s  1960s  1970s  1980s  1990s  2000s  2010s  2020s
 By director  Godard   Wes Anderson   Spielberg …   (from the crew of the films)

ITEM (1280)
 Breathless                                              ┌─────────┐
 À bout de souffle                                       │ poster  │
 1960   1h 30m   NR   Crime, Drama                        │  w342   │
 IMDb 7.6   Rotten Tomatoes 97%   Letterboxd 4.0          └─────────┘
 [ Play, ends 22:11 ]   [ Start over ]   [ More ]
 
 A small-time thief steals a car and impulsively murders a motorcycle
 policeman. …                                    (serif, 62ch, leading 1.6)
 
 Directed by       Jean-Luc Godard
 Written by        Jean-Luc Godard, François Truffaut, Claude Chabrol
 Photographed by   Raoul Coutard
 With              Jean-Paul Belmondo, Jean Seberg, and 28 more
 
 ▸ File and playback      1080p, HEVC, plays in this browser as a remux; details from TMDB
```

**Phone.** The Library becomes a typeset index: one line per film with title, year and running time, and about 14
films per screen instead of 2. Posters are a toggle.

**Motion: print, not film.** Almost static. The one moment: posters fade in (opacity, `--dur-base`) under their title
cards as they arrive. The clock times update each minute without animation. Reduced motion means no fades.

```css
:root {
  /* primitives */
  --bottle-950: #0e2420; --bottle-900: #15302b; --bottle-800: #1e3d37; --bottle-700: #2e4f48;
  --paper-100: #eee8da; --paper-400: #a8b8ae;
  --ticket-300: #f4a6b7; --ticket-950: #3a0f1a;
  --signal-ok: #8fd3a8; --signal-warn: #f2c46b; --signal-danger: #ff8f86;
  --r-2: 2px; --r-6: 6px; --r-12: 12px;
  --ease-print: cubic-bezier(0.2, 0.8, 0.2, 1);

  /* semantic */
  --surface-0: var(--bottle-950); --surface-1: var(--bottle-900); --surface-2: var(--bottle-800);
  --scrim: rgb(14 36 32 / 0.82);
  --text: var(--paper-100); --text-muted: var(--paper-400); --text-on-accent: var(--ticket-950);
  --shadow-text: none;
  --line: var(--bottle-700); --line-strong: var(--paper-400);
  --accent: var(--ticket-300); --tint: var(--accent);
  --ok: var(--signal-ok); --warn: var(--signal-warn); --danger: var(--signal-danger);
  --focus-ring: var(--ticket-300); --focus-width: 2px; --focus-halo: none;
  --radius-s: var(--r-2); --radius-m: var(--r-6); --radius-l: var(--r-12);
  --shadow-1: none; --shadow-2: 0 16px 40px rgb(0 0 0 / 0.45);
  --dur-fast: 120ms; --dur-base: 200ms; --dur-slow: 320ms; --dur-scene: 320ms;
  --ease-out: var(--ease-print); --ease-in-out: cubic-bezier(0.6, 0, 0.3, 1);
  --target-min: 44px; --card-w: clamp(9rem, 16vw, 13rem);

  /* type */
  --font-display: 'Avenir Next Condensed', 'Barlow Condensed', 'Arial Narrow', sans-serif;
  --font-text: 'Iowan Old Style', 'Source Serif 4', Charter, Georgia, serif;
  --font-mono: ui-monospace, 'SF Mono', Menlo, monospace;
  --weight-text: 400; --weight-strong: 600; --weight-display: 700;
  --tracking-display: -0.01em;
  --leading-tight: 0.95; --leading-snug: 1.2; --leading-body: 1.6;
  --step--1: clamp(0.833rem, 0.831rem + 0.011vw, 0.844rem);
  --step-0: clamp(1rem, 0.971rem + 0.128vw, 1.125rem);
  --step-1: clamp(1.2rem, 1.131rem + 0.307vw, 1.5rem);
  --step-2: clamp(1.44rem, 1.311rem + 0.573vw, 2rem);
  --step-3: clamp(1.728rem, 1.512rem + 0.961vw, 2.665rem);
  --step-4: clamp(2.074rem, 1.732rem + 1.516vw, 3.552rem);
  --step-5: clamp(2.488rem, 1.97rem + 2.304vw, 4.735rem);
  --step-6: clamp(2.986rem, 2.219rem + 3.41vw, 6.31rem);
}
```

---

### B. House lights: the backdrop is the interface

**Concept.** The screen is the film. Each view is one full-bleed still of the focused title, with nothing in the top
two thirds. Everything you read sits in the lower third, **set like subtitles**: centred, white, with a subtitle's
shadow instead of a panel behind it. The film's own TMDB logo is its title; there are 245 of them. Browsing means
moving along a filmstrip of 16:9 stills at the bottom, and the whole screen changes with focus. It is built for the TV
and the iPad on the sofa, and it is still usable on a phone, where the filmstrip becomes a vertical reel.

**Signature: subtitles as the UI.** All text in the lower third uses one line style: the subtitle. On the item page, cast
and crew are a **credit roll**: centred name and role pairs on a central axis, like end credits. That replaces the
grey portrait slabs (old films have no portraits) with something that looks right for every film from 1922 on.

**Palette.** True black for OLED, white, and the film's own colour.

| Name | Hex | Role |
|---|---|---|
| Blackout | `#000000` | page: true black, so OLED TVs and iPhones switch pixels off |
| Subtitle | `#FFFFFF` | text |
| Silver | `#B8BCC4` | muted text (11:1) |
| Leader | `#D8C9A8` | default accent: the warm grey of film leader, used when the film has no colour yet |
| Tint | runtime | the accent per film, from the artwork (`--tint`) |
| Subtitle yellow | `#FFE14D` | **the focus ring only**: "this is the line being read now" |

**Type.**
- **Display:** the film's logo. With no logo, *Futura* (macOS and iOS), the typeface of Kubrick's and Wes Anderson's
  title cards, falling back to self-hosted *Jost* (OFL).
- **Text:** `system-ui` (SF Pro on Apple), semibold for subtitle lines.
- **Mono:** none on viewer pages.

**Layout.** Centred on one axis, the subtitle axis. It is the only centred direction of the three.

```
HOME (TV 1920 / Mac 1280)
┌──────────────────────────────────────────────────────────────────┐
│                                                                  │
│          full-bleed backdrop of the focused title (w1280),       │
│          nothing on it above the lower third                     │
│                                                                  │
│                        [ RAIDERS logo ]                          │
│                    1981   1h 55m   Spielberg                     │
│                      Play      More about it                     │
│  ▕▔▔▔▔▔▕▔▔▔▔▔▕▔▔▔▔▔▕▔▔▔▔▔▕▔▔▔▔▔▕▔▔▔▔▔▕▔▔▔▔▔▕▔▔▔▔▔▕                   │
│  ▕     ▕     ▕ ███ ▕     ▕     ▕     ▕     ▕     ▕  filmstrip:      │
│  ▕▁▁▁▁▁▕▁▁▁▁▁▕▁▁▁▁▁▕▁▁▁▁▁▕▁▁▁▁▁▕▁▁▁▁▁▕▁▁▁▁▁▕▁▁▁▁▁▕  16:9 stills     │
│     Continue    New    Films    Shows    Search     reels: ↑ ↓    │
└──────────────────────────────────────────────────────────────────┘

ITEM
┌──────────────────────────────────────────────────────────────────┐
│ ‹ Back                                                           │
│                    backdrop, full-bleed                          │
│                    [ BREATHLESS logo ]                           │
│              Wild! Violent! Outspoken and Honest!                │
│       1960   1h 30m   NR   IMDb 7.6   Rotten Tomatoes 97%         │
│               ( Play )    Start over    More                     │
├──────────── scroll: the house lights come up ────────────────────┤
│        synopsis, centred column, 56ch                            │
│                 Directed by   Jean-Luc Godard                    │
│                  Written by   Jean-Luc Godard                    │
│                               François Truffaut                  │
│                  Michel Poiccard   Jean-Paul Belmondo            │
│                Patricia Franchini   Jean Seberg                  │
│        File and playback ▸          (quiet, at the end)          │
└──────────────────────────────────────────────────────────────────┘
```

**Phone.** The backdrop takes the top half, the subtitle block sits below it, and the filmstrip is a vertical reel of
stills with titles.

**Motion: house lights.** One orchestrated moment: pressing Play dims the room. The scrim darkens and the subtitles fade
over `--dur-scene` (1.2 s), then the video fades up. Focus on the filmstrip crossfades the backdrop (`--dur-slow`),
debounced by 150 ms so that holding an arrow key doesn't fire NAS reads. On the item page, scrolling raises the house
lights: a pre-blurred w300 copy of the still fades in under the text. Never `backdrop-filter`. Reduced motion means cuts.

**Artwork rules.** Prefer textless backdrops (the API asks TMDB for `include_image_language=null` first; this is an API
ask, and the design must cope without it). Check each logo's luminance once, and set a dark logo (Breathless) on a
soft light glow, or invert it.

```css
:root {
  /* primitives */
  --black: #000000; --white: #ffffff; --silver-300: #b8bcc4;
  --leader-200: #d8c9a8; --subtitle-yellow: #ffe14d;
  --glass-1: rgb(255 255 255 / 0.08); --glass-2: rgb(255 255 255 / 0.16);
  --signal-ok: #8ee0a8; --signal-warn: #ffd166; --signal-danger: #ff8a80;
  --r-4: 4px; --r-8: 8px; --r-14: 14px;

  /* semantic */
  --surface-0: var(--black); --surface-1: var(--glass-1); --surface-2: var(--glass-2);
  --scrim: rgb(0 0 0 / 0.72);
  --text: var(--white); --text-muted: var(--silver-300); --text-on-accent: var(--black);
  --shadow-text: 0 1px 2px rgb(0 0 0 / 0.9), 0 0 14px rgb(0 0 0 / 0.65);
  --line: rgb(255 255 255 / 0.18); --line-strong: rgb(255 255 255 / 0.5);
  --accent: var(--leader-200); --tint: var(--accent);
  --ok: var(--signal-ok); --warn: var(--signal-warn); --danger: var(--signal-danger);
  --focus-ring: var(--subtitle-yellow); --focus-width: 3px; --focus-halo: 0 0 0 1px var(--black);
  --radius-s: var(--r-4); --radius-m: var(--r-8); --radius-l: var(--r-14);
  --shadow-1: none; --shadow-2: 0 24px 60px rgb(0 0 0 / 0.6);
  --dur-fast: 150ms; --dur-base: 300ms; --dur-slow: 450ms; --dur-scene: 1200ms;
  --ease-out: cubic-bezier(0.25, 0.6, 0.3, 1); --ease-in-out: cubic-bezier(0.65, 0, 0.35, 1);
  --target-min: 44px; --card-w: clamp(10rem, 18vw, 18rem);

  /* type */
  --font-display: Futura, 'Futura PT', Jost, 'Century Gothic', sans-serif;
  --font-text: system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif;
  --font-mono: ui-monospace, 'SF Mono', Menlo, monospace;
  --weight-text: 400; --weight-strong: 600; --weight-display: 500;
  --tracking-display: 0.02em;
  --leading-tight: 1.05; --leading-snug: 1.25; --leading-body: 1.5;
  --step--1: clamp(0.833rem, 0.795rem + 0.171vw, 1rem);
  --step-0: clamp(1rem, 0.942rem + 0.256vw, 1.25rem);
  --step-1: clamp(1.2rem, 1.116rem + 0.372vw, 1.563rem);
  --step-2: clamp(1.44rem, 1.322rem + 0.526vw, 1.953rem);
  --step-3: clamp(1.728rem, 1.563rem + 0.732vw, 2.441rem);
  --step-4: clamp(2.074rem, 1.848rem + 1.003vw, 3.052rem);
  --step-5: clamp(2.488rem, 2.182rem + 1.36vw, 3.815rem);
  --step-6: clamp(2.986rem, 2.575rem + 1.828vw, 4.768rem);
}
```

---

### C. Shelf: the living room

**Concept.** A calm, high-legibility interface for the TV and the remote, which also works at the desk. The collection
is what it would be in a living room: **a shelf**. Each film is a spine, a narrow vertical band in its cover's colour
with its title set sideways. Spines need no image, so a shelf of 60 films paints instantly from the JSON. Lean back, use
the arrows, read from 3 m. One decision per screen. Nothing moves unless the remote moved it.

**Signature: pull a spine.** When focus lands on a spine, it slides forward and turns: the spine widens into the cover
(w342), and the film's line appears below the shelf (title, year, running time, director). Left and right move along a
shelf; up and down move between shelves (New, Unwatched, by decade, by collection, Shows). Spine colour is the
artwork's `art.color` when the server has it. Without it, the colour is a deterministic pick from six muted
**bookcloth** tones, keyed by title id, so the design works on today's API. (A colour for every title is an API ask.)

**Palette.** A lamp-lit room after dark.

| Name | Hex | Role |
|---|---|---|
| Dusk | `#1A2130` | page: blue-slate, the room with the lights low |
| Shelf | `#232C3D` | raised surfaces and the shelf board |
| Linen | `#EEF1F6` | text (14.2:1) |
| Fog | `#A3AEC2` | muted text (7.2:1) |
| Lamp | `#FFC56B` | focus and the accent: the reading lamp falling on the chosen film (10.3:1) |
| Lamp ink | `#2A1A00` | text on Lamp |

The bookcloth tones are `#7A4E3A`, `#4F6B58`, `#3F5A7A`, `#7A6A3F`, `#6A4A6E` and `#5A5F66`. They carry white text at
5.3:1 to 7.5:1.

**Type.** One family, chosen for legibility: *Atkinson Hyperlegible Next* (SIL OFL, Braille Institute, 2025),
self-hosted in two weights (about 40 KB each as subset woff2). It has clear letterforms for older relatives and for 3 m.
The fallback is `ui-rounded` (SF Rounded), then `system-ui`. *Atkinson Hyperlegible Mono* (OFL) is for the file details.
The type is bigger than the other two directions at every size, with a 1.5 rem base on a TV.

**Layout.** Left-aligned, in big blocks, at most three things per row on a TV.

```
HOME (TV 1920)
 ply                                                         Search   Settings
 
 ┌──────────────────┐   Silo
 │  episode still   │   Season 2, episode 4
 │  ▓▓▓▓▓▓▓▓░░░░░░  │   32 minutes left
 └──────────────────┘   ( Resume )    All episodes
 
 New on the shelf
 ║▓║█║░║▓║█║▓║░║█║▓║█ ┌────────┐ ║▓║░║█║▓║█║▓║░║█║▓║
 ║B║C║U║R║S║T║A║F║N║  │ cover  │ ║ ║ ║ ║ ║ ║ ║ ║ ║ ║   the focused spine is pulled
 ║r║h║n║a║p║a║l║l║o║  │  w342  │ ║ ║ ║ ║ ║ ║ ║ ║ ║ ║   forward and turned to its cover
 ║e║u║d║i║e║r║c║e║s║  └────────┘
 ══════════════════════════════════════════════════  the shelf board
 Breathless    1960   1h 30m   Jean-Luc Godard
 
 Unwatched films ▸     The 1960s ▸     Indiana Jones ▸     Shows ▸

ITEM (TV 1920)
 ┌────────────┐   Breathless
 │            │   1960   1h 30m   NR
 │   cover    │   IMDb 7.6     Rotten Tomatoes 97%
 │   2:3      │
 │   w500     │   ( Play )                 56 px, lit by the lamp when focused
 │            │   ( Start over )
 │            │   ( More about this film )
 └────────────┘
                  A small-time thief steals a car and impulsively
                  murders a policeman. …   (three lines; More opens a sheet)
 Cast   Jean-Paul Belmondo   Jean Seberg   Daniel Boulanger   …   (names, no portraits)
```

**Phone.** The spines lie flat: each shelf is a list whose rows are a colour band plus the title, horizontal and
readable. A tap opens the cover sheet.

**Motion: calm and physical.** Only in answer to input. The spine pull is `transform` and `opacity` over
`--dur-base`, with the cover image swapped in when loaded. Focus moves by translating the shelf, never by jumping
scroll. No autoplay, no carousels that advance on their own, no parallax. Reduced motion means the cover appears in place.

```css
:root {
  /* primitives */
  --dusk-950: #1a2130; --dusk-900: #232c3d; --dusk-800: #2e3950; --dusk-700: #3b4760;
  --linen-50: #eef1f6; --fog-300: #a3aec2;
  --lamp-300: #ffc56b; --lamp-950: #2a1a00;
  --cloth-1: #7a4e3a; --cloth-2: #4f6b58; --cloth-3: #3f5a7a;
  --cloth-4: #7a6a3f; --cloth-5: #6a4a6e; --cloth-6: #5a5f66;
  --signal-ok: #8fd6a6; --signal-warn: #ffd27a; --signal-danger: #ff9a8f;
  --r-6: 6px; --r-10: 10px; --r-18: 18px;

  /* semantic */
  --surface-0: var(--dusk-950); --surface-1: var(--dusk-900); --surface-2: var(--dusk-800);
  --scrim: rgb(26 33 48 / 0.85);
  --text: var(--linen-50); --text-muted: var(--fog-300); --text-on-accent: var(--lamp-950);
  --shadow-text: none;
  --line: var(--dusk-700); --line-strong: var(--fog-300);
  --accent: var(--lamp-300); --tint: var(--accent);
  --ok: var(--signal-ok); --warn: var(--signal-warn); --danger: var(--signal-danger);
  --focus-ring: var(--lamp-300); --focus-width: 4px;
  --focus-halo: 0 0 48px 8px rgb(255 197 107 / 0.3);
  --radius-s: var(--r-6); --radius-m: var(--r-10); --radius-l: var(--r-18);
  --shadow-1: 0 2px 0 rgb(0 0 0 / 0.35); --shadow-2: 0 18px 48px rgb(0 0 0 / 0.5);
  --dur-fast: 120ms; --dur-base: 200ms; --dur-slow: 280ms; --dur-scene: 280ms;
  --ease-out: cubic-bezier(0.3, 0.7, 0.3, 1); --ease-in-out: cubic-bezier(0.6, 0, 0.3, 1);
  --target-min: 48px; --card-w: clamp(2.5rem, 3.2vw, 4rem); /* a spine */

  /* type */
  --font-display: 'Atkinson Hyperlegible Next', ui-rounded, 'SF Pro Rounded', system-ui, sans-serif;
  --font-text: var(--font-display);
  --font-mono: 'Atkinson Hyperlegible Mono', ui-monospace, Menlo, monospace;
  --weight-text: 400; --weight-strong: 700; --weight-display: 700;
  --tracking-display: 0;
  --leading-tight: 1.1; --leading-snug: 1.3; --leading-body: 1.55;
  --step--1: clamp(0.885rem, 0.813rem + 0.323vw, 1.2rem);
  --step-0: clamp(1.063rem, 0.962rem + 0.449vw, 1.5rem);
  --step-1: clamp(1.275rem, 1.137rem + 0.615vw, 1.875rem);
  --step-2: clamp(1.53rem, 1.342rem + 0.835vw, 2.344rem);
  --step-3: clamp(1.836rem, 1.584rem + 1.122vw, 2.93rem);
  --step-4: clamp(2.203rem, 1.867rem + 1.496vw, 3.662rem);
  --step-5: clamp(2.644rem, 2.198rem + 1.983vw, 4.578rem);
  --step-6: clamp(3.172rem, 2.584rem + 2.615vw, 5.722rem);
}
```

## 8. Review against the brief

Each first draft was checked against the generic defaults. Here is what changed and why.

- **A, Repertory.** The first draft was cream paper, an oxblood "stamp" red, Didot headlines and hairline-ruled
  newspaper columns. That is the cream-plus-terracotta look and the broadsheet look at once, and a light page is wrong
  for evening viewing anyway. It became dark bottle-green card stock with a ticket-pink accent. Didot became a condensed
  sans that comes from cinema programmes and marquee letters. Structure now comes from type size and spacing, not rules.
  There are no all-caps eyebrows, and titles keep their own case.
- **B, House lights.** The first draft was pure black with subtitle yellow on everything (buttons, links, the active
  reel). That is the near-black-with-one-bright-accent look. Now the accent is each film's own colour (`--tint`), with
  Leader as the neutral fallback, and yellow is kept for the focus ring alone, where it means what it means in
  subtitles. Frosted-glass panels (an Apple TV imitation) were cut: they cost GPU on TVs and phones, and a subtitle
  shadow does the same job.
- **C, Shelf.** The first draft was rounded cards with soft grey shadows on slate, one radius for everything, which is
  the SaaS card kit. Cards became spines: no card chrome, and no shadow except the lamp halo on focus. Radii now follow
  a hierarchy: spine 6 px, cover 10 px, sheet 18 px.
- **All three.** No middle-dot metadata (facts are separated by space, as now), no arrows appended to links, no numbered
  section markers, no monospace for small labels, no gradient washes as decoration.

## 9. Principles common to all three

**Performance.** Playback first, then browsing, then the queue.
- First paint of Home, with text and placeholders, comes from the JSON alone, under 1 s on the Mac with a cold NAS.
  Images never block layout.
- Every image has a size per slot (`w185`, `w342` or `w500` for posters; `w780` then `w1280` for backdrops) through
  `srcset` and `sizes`. Only the one hero image gets `fetchpriority="high"`; everything else is lazy.
- Placeholders are designed, not grey: A uses type, B uses the tint colour, C uses spines.
- Animate only `opacity` and `transform`. No `backdrop-filter` on large areas, no animation library, and no JS on
  scroll except one IntersectionObserver per list.
- Long lists use `content-visibility: auto`. The 1,866 episodes and the activity log are virtualised past 200 rows.
- Debounce focus-driven image loads (150 ms) so that holding an arrow key doesn't queue NAS reads.

**Focus and the remote.**
- One focus style, built from the tokens (`--focus-ring`, `--focus-width`, `--focus-offset`, `--focus-halo`), on every
  focusable element.
- Spatial navigation: arrows move between focusables in grids and rails, Enter activates, Back goes back.
- On a route change, focus goes to the page's `h1` or first action. On Back, it returns to the card it left.
- `data-input="remote"` is set the first time arrow keys are used with no pointer, and raises `--target-min` and the halo.

**Accessibility.**
- WCAG 2.2 AA. The contrast of each direction's pairs is checked above; recheck any new pair.
- Use `prefers-reduced-motion` and `prefers-contrast: more` (which uses `--line-strong` and turns off scrims under text).
- Text zoom to 200 % without loss. Real headings and landmarks. Every icon button has a label. Logos used as titles
  have the title as their `alt`.

**Attributions.**
- Rating chips show the source's name or mark and link to its page.
- Settings and an About panel carry the TMDB notice and logo, TheTVDB credit, the IMDb attribution and dataset date, and
  the MDBList credit.
- The detail page names its source quietly, inside "File and playback", not beside the title.

**DRY, replaceable CSS.**
- All design values live in `web/src/styles/tokens.css`: colours, the spacing scale, radii, shadows, motion durations
  and easing, and z-index. They are structured primitive, then semantic, then type, as in the blocks above.
- Components use only semantic tokens, and a small set of shared component and utility classes (`.btn`, `.chip`,
  `.meta`, `.sr-only`, `.tabular`, `.stack`, `.cluster`). They never use a raw colour, size, radius or duration.
- **Fonts are defined once:** `--font-display`, `--font-text` and `--font-mono`, with `@font-face` for self-hosted files
  in `tokens.css` only. Components never name a font family or a raw size. They use `--step-N` with `--leading-*` and
  `--weight-*`.
- Swapping the theme or the fonts means editing one file. Each direction above is exactly such a file, over the same
  components and the same semantic names. Stylelint enforces this.

**Copy.** Sentence case and plain verbs. Name things by what people do ("Resume", "Start over", "Fix the match"). An
empty state says what to do next.

## 10. Sprint materials (not in the repo)

These are private to the prototypes. They are taken from the owner's library and must never be committed.

| What | Where (in the session scratchpad) |
|---|---|
| Current UI screenshots, 1280 and 390 px: Home, Movies, Item, Show, Settings | `design/current/*.png` |
| `/api/items`, `/api/shows`, `/api/genres`, `/api/continue` (empty today), `/api/upcoming`, `/api/info` | `design/data/*.json` |
| 14 film details (`/api/items/{id}`) with cast, crew and collections, from 1922 to 2022 across 16 genres; 2 collections; 2 shows; 1 person | `design/data/items/`, `collections/`, `shows/`, `people/` |
| Posters (w342), backdrops (w1280) and logos for 38 titles, with a manifest from `titleId` to file names | `design/data/img/`, `design/data/img-manifest.json` |

Notes on the data:
- No film has progress yet, so Continue is empty. Prototypes should fake one episode and one film in progress to show job 1.
- No item has `art.color` yet. Compute a colour from the local poster in the prototype; production falls back as each
  direction says.
- Item details include `path`, the file's location on the NAS. Don't show it on screen.
