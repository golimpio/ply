# UI redesign plan: visual direction before implementation

Status: proposal for owner review, 6 November 2026. No production UI changes are authorized by this document alone.

This plan supplements [UI-BRIEF.md](UI-BRIEF.md), [DESIGN.md](DESIGN.md), and [M10-RATINGS.md](M10-RATINGS.md). It records the owner's latest feedback and defines how an agent should explore the design. Earlier palettes, typography choices, component-library proposals, and the blanket rejection of glass in UI-BRIEF.md are prior design proposals, not confirmed owner preferences.

## 1. The brief

Improve the quality and identity of Ply without making a personal collection harder to browse. The ambition is a calm, distinctive media product with excellent artwork composition, typography, spacing, controls, and transitions.

“Award winning” expresses an aspiration, not an acceptance test. Make the craft visible in the work; do not claim that a trend, framework, or effect guarantees it.

### Confirmed owner preferences

- A consistent experience across desktop, tablet, and phone, adapted to their input and available space.
- Minimal visual clutter and pragmatic language. Editorial headings must describe the actual selection beneath them.
- Keep a useful home/journal; it already exists.
- Selecting a film opens useful film information before playback.
- TV details must make seasons and episodes immediately practical to navigate.
- Ratings deserve intentional space, including multiple sources where available.
- Browse hundreds of films and shows with continuous scrolling and alphabetical jump navigation. Numbered pages are a regression.
- Plex provides a useful baseline. Change familiar behavior when the replacement improves the experience.
- The earlier Cinema and Cinematheque explorations had appealing atmosphere. Their catalogue navigation was inadequate.
- Restrained translucent controls are worth exploring, as in the Infuse reference.

### Questions to answer visually

- How much artwork can the home show while remaining quiet and useful?
- Does a slightly larger first row improve hierarchy without pushing everything else away?
- What typography and composition give Ply its own identity?
- Do small glass controls improve the selected composition compared with solid controls?
- How can a detail page feel cinematic while keeping ratings and episodes readable?

Do not translate these questions directly into a large implementation task.

## 2. How to use the references

| Reference | Explore | Do not copy automatically |
| --- | --- | --- |
| [TV detail reference](https://cdn.dribbble.com/userupload/19869774/file/original-e7a4b396d4a1a938f9c70f7e7da12f27.png?resize=752x&vertical=center) | Clear poster, title, facts, ratings, season selection, and immediately accessible episode artwork | Tilted presentation geometry or controls that only work in a static mockup |
| [Home reference](https://cdn.dribbble.com/userupload/31980885/file/original-5bc8d07bf9e1bc55797c61acbfc2cc51.png?resize=752x&vertical=center) | Relative scale and emphasis in the top row | Widget density, promotional clutter, social links, or coverflow as a default requirement |
| Owner-attached Wednesday concept | Artwork-led atmosphere, clear title/action hierarchy, restrained foreground | Netflix branding, marketing content, or a mandatory enormous hero |
| Owner-attached Infuse/Mad Men screenshot | Dedicated facts and ratings area, restrained translucent controls, visible episode strip | Marketing copy surrounding the product screenshot or an unlabelled wall of icons |

These are references for particular qualities, not instructions to clone another product. The attached images were supplied in this conversation; do not commit their temporary local paths or private library snapshots.

## 3. A proposed direction to test

Artwork first, minimal chrome, useful facts, precise typography.

Keep the functional structure stable enough to learn once. Establish identity through composition, type, artwork crops, material, and the relationship between browsing and details. A new navigation invention is not a prerequisite for a distinctive design.

Cinema and Cinematheque can describe two visual studies of the same product:

- **Cinema:** atmospheric image composition, generous space around the selected title, quiet supporting chrome.
- **Cinematheque:** more typographic structure and collection context, while retaining artwork and practical browsing density.

These are working labels, not approved themes. Use identical content and tasks so differences in taste are easy to compare. Avoid turning Cinematheque into a newspaper, technical dashboard, or catalogue modal.

### Home

- Give continuation priority when there is actual progress. Provide an honest fallback when there is none.
- Test one modestly larger first row against the current hierarchy. Approximately 1.2–1.35 times the secondary card size is a starting experiment, not a specification.
- Choose a restrained opening composition. An oversized hero, enlarged first row, and several competing widgets should not all demand attention.
- Keep “Continue watching” and “Recently added” descriptive. Group show updates so one imported season does not overwhelm the page.
- Keep full collection access obvious.
- Do not introduce automatic layout changes based on guessed mood or time of day. Any later adaptive behavior must remain predictable and controllable.
- No decorative slogans, autoplaying previews, or invented editorial selections.

### Collection browser

- Preserve continuous browsing and fast alphabetical jumps when sorted by title.
- A letter jumps to a position in the current collection; it must not silently become a starts-with filter.
- Other sort orders need navigation appropriate to that order; do not show a misleading alphabetical index.
- Keep sort, filters, and density accessible without a permanent wall of controls.
- Returning from details restores the same item, scroll position, keyboard focus, sort, filters, and density.
- Compare browsing density with the current UI using the same viewport and collection. Do not reduce usefulness to make a screenshot look cleaner.
- Cover missing artwork, long titles, non-Latin titles, and large collections.
- No numbered pages or required “Load more” clicks.

### Film details

- Compose title/logo, year, runtime, essential facts, ratings, short synopsis, and primary action as one clear hierarchy.
- Use Play or Resume according to real state; make starting over accessible where relevant.
- Provide a stable place for ratings rather than squeezing them into leftover space.
- Put cast, related navigation, versions, and technical information in a clear secondary hierarchy.
- Keep stream and file details available to people who need them without making them the dominant visual language.
- Use a route-backed detail view that works on small screens and supports Back. A small catalogue popup is not the primary browsing experience.

### Show and episode details

- Keep show identity, selected season, and selected episode identity distinct.
- Make season navigation visible and place episode thumbnails or a readable episode list near the main information.
- Clearly label episode number, title, runtime, watched/progress state, and available episode rating.
- Retain the selected season on return. Changing season does not start playback.
- Use the next relevant episode for the primary action only when the state supports that choice.
- Avoid confusing a show rating with the selected episode's rating.
- Provide a compact alternative to a large thumbnail strip for long seasons and narrow screens.

### Ratings

Reuse the current Rating.svelte, Ratings.svelte, rating preferences, and RatingEntry contract. This is a presentation task; do not rebuild the M10 provider system.

- Use one named primary rating where a collection card needs a compact signal.
- Details can show the user's ordered available sources, with native scales.
- Distinguish Rotten Tomatoes critics and audience scores.
- Wrap gracefully on mobile; reserve a flexible region rather than a fixed number of provider slots.
- Missing ratings must not appear as zero, fabricated values, or a row of permanently empty badges.
- Preserve attribution and source links where available. Ratings must not block the first useful rendering.
- In the current M10 contract, TVDB supplies metadata/popularity rather than an exposed user-rating source. Do not fabricate a TVDB rating. If provider support changes later, revisit the contract explicitly.

### Material and motion

Compare translucent and opaque controls in the same composition. Glass is an experiment, not a requirement or blanket prohibition.

- Limit translucency to small functional surfaces where it helps separation.
- Test over bright, dark, and busy artwork, with an opaque fallback.
- Avoid full-page blur, constant pointer effects, and ornamental distortion.
- Use motion to preserve orientation between a selected card and its detail view, if that improves the experience.
- Respect reduced motion, visible focus, contrast, and touch targets.
- Keep labels and interaction models consistent across devices. Mobile should reorganize the hierarchy rather than shrink a desktop screenshot.
- Remote playback should state the active target clearly. Do not change Home into a controller without an explicit, understandable transition.

## 4. Execution in stages

### Stage 0 — Establish the baseline

Read AGENTS.md and the linked project specifications. Inspect the current Home, collection, film, show, and mobile views. Record what already works and what the redesign must preserve.

Select representative, authorized artwork and data: a long title, a short title, bright and dark backdrops, logos with different proportions, missing art, sparse ratings, multiple seasons, and progress/no-progress states.

Use the current image-serving route or an authorized private snapshot. Keep private paths, tokens, viewing history, and data exports outside this public repository. Label any simulated state. Existing snapshot material may have no real progress; never imply that invented progress is live user data.

Output: a short baseline with screenshots, content selection, and constraints. List any proposed new data separately from what exists.

### Stage 1 — Art direction, before a functional prototype

Produce two genuinely different high-fidelity visual studies using the same real artwork, text, and available ratings:

1. Home.
2. Film details.
3. Show details with seasons and episodes visible.

Show all three on desktop and phone for each study, in a comparison sheet plus readable individual views. Include a compact browser view to demonstrate that the direction can support collection density. Use enough real content to assess composition; generic gradient rectangles cannot establish a cinematic design.

A small glass-versus-solid control comparison can accompany the studies. Do not build three complete applications or select a new framework to produce this work.

Present a clear recommendation and explain a few concrete tradeoffs. Do not call either result “premium” as a substitute for showing its quality.

**Review point:** the owner identifies what to keep, change, and remove using specific visual examples. Record the selected composition and rejected alternatives. If neither study is convincing, revise the art direction before implementing production components.

### Stage 2 — Prove the interaction

Build one focused prototype of the chosen direction, with the existing route structure and representative collection scale.

Demonstrate:
- Home to collection to film details and Back, with position preserved.
- Continuous collection scrolling and an alphabetical jump.
- Filtering and sorting without losing orientation.
- Show selection, season switching, episode selection, and return.
- Ratings with many, few, and no available providers.
- Keyboard, touch, and reduced-motion behavior.

If proposing a signature interaction, demonstrate it alongside the current behavior and explain the benefit. A beautiful transition that loses position or slows selection fails this stage.

Output: a runnable prototype, captured states, and a list distinguishing working behavior from simulation. Do not disguise missing functionality behind a polished screenshot.

### Stage 3 — Validate visual and practical quality

Compare the chosen direction against the baseline using the same tasks and content.

- Desktop, phone, and tablet layouts; narrow-width and 200% zoom stress checks.
- Readability over bright/dark artwork, long text, missing images, and delayed images.
- Ratings and episode navigation remain visible and understandable.
- Keyboard focus, Back behavior, contrast, touch targets, and reduced motion.
- Time and effort to resume, find a known title, jump to a letter, inspect ratings, and choose an episode.
- Initial useful content appears before slow artwork; reserve image dimensions and avoid layout jumps.

Visual approval is an owner's taste decision. Navigation correctness, accessibility, and performance require observable evidence. Record both separately instead of giving the design an invented aesthetic score.

### Stage 4 — Implement the approved direction in small slices

Implement only after the visual direction and interaction behavior are reviewed.

Suggested order: shared visual primitives, collection/detail navigation, film details, show details, Home, then remaining responsive and state refinements. Adjust order to the actual dependency audit.

Reuse current routes, playback lifecycle, API contracts, rating components, and image delivery. Extract tokens from the selected design. Do not make typography, palette, or a component-library migration the first irreversible decision.

Follow project testing and independent code-review requirements. Every fix needs its regression test and mutant demonstration as required by AGENTS.md. Verify the rendered result in the browser; passing build checks cannot establish visual quality. Leave no preview processes running.

## 5. Definition of a successful first milestone

The first milestone is complete when:

- Two coherent visual directions can be compared using real content.
- Home is quieter than the dense reference while retaining clear priorities.
- Film details have a deliberate information and ratings hierarchy.
- Show details make seasons and episodes practical.
- A browser example demonstrates usable density and continuous navigation.
- Desktop and phone belong to the same visual system.
- Every proposal states what exists, what is simulated, and what would need new data.
- The owner can point to specific choices worth carrying forward.

It is not complete merely because an agent produced many screens, introduced glass, or used the word “futuristic.”

## 6. Prompt for the next design agent

> Read AGENTS.md, docs/UI-REDESIGN-PLAN.md, docs/UI-BRIEF.md, and the relevant current UI. Execute Stage 0 and Stage 1 only. Create two high-fidelity visual directions for the same Home, film detail, and show detail content, on desktop and phone, plus a collection-browser density example. Use authorized real artwork and available ratings; label simulated state and keep private data out of the public repository. Preserve continuous collection browsing, alphabetical jumps, practical seasons/episodes, and plain copy. Show a recommendation with concrete tradeoffs. Do not change production UI, add a framework, or proceed to implementation before visual review.

## 7. Evidence and boundaries

This proposal draws on the owner's reference images and feedback, the current UI brief and design specification, the M10 ratings specification, and the existing web rating components/API types inspected on 6 November 2026.

It does not claim a fresh end-to-end audit of all current screens, measured usability improvements, implemented handoff features, or visual approval. Those are explicit outputs of the stages above.
