# Agent rules for ply

## Rule 1, above every other: always use plumb MCP

Every agent working on this project uses the **plumb MCP tools** for everything they cover:
reading, searching, editing, building, testing and git. This applies to the main session, to every
subagent and to every peer session. Native tools (Bash, Read, Edit, Write, Grep) are only for what
plumb cannot do. When you fall back to a native tool, say why in one line, and report the gap with
`tool_feedback`.

| Task | Use |
|---|---|
| Start of every session or subagent | `session_start` (subagents: `detail: "brief"`, `session_id: "<conversation>/<tag>"`) |
| Read a file | `read_file` (or `read_symbol`, `file_outline`) |
| Edit / create | `edit_file` / `write_file`, passing `expected_mtime` or `expected_sha` from the read. Use `transaction_apply` for multi-file changes. |
| Search | `search_in_files`, `find_files`, `workspace_symbols`, `find_references`, `workspace_search` |
| Compile errors | `diagnostics`, or `fail_on_new_errors` on the edit. `run_task build` is the final word: the LSP is sometimes stale. |
| Build and test Go | `run_task` (`language: "go"`, slots `build`, `test`, `lint`, `e2e` = integration, `verify`), plus `run_command` `go-vet`, `go-test-race`, `go-test-integration`, `go-tidy`, `ply-build` |
| Build and test the web UI | `run_command` `web-install`, `web-check`, `web-build`, `web-test`, `web-e2e` |
| Which tests to run | `topology_affected` |
| Git | the plumb `git` tool (`status`, `diff`, `log`, `add`, `commit`). Push only when the user asks. |
| Talk to a peer agent | `leave_note` / `check_messages` |
| Project memory | `search_memories`, `write_memory` |

What still needs a native tool (shell): `ffmpeg`/`ffprobe` fixtures, `curl` against a running ply, `make`
targets that have no plumb command yet, and Xcode/`xcodebuild`.

## Other rules
- **The repository is public.** Never commit API keys, PINs, tokens, `.plumb/memories/` or anything from the
  user's data directory. Keys belong in the settings table (through the web UI), or in flags or environment variables.
- **Each piece of code is reviewed by someone other than its author.**
- **Every fix comes with a test that fails without it.** Prove it with a mutant: break the code, watch the test fail,
  restore it.
- **An absence assertion needs a positive control.** For example, before asserting that an item was deleted, assert that it existed.
- **Leave no background processes running.** Temporary fixtures go in `t.TempDir()` or the session scratchpad.
- **Strictly DRY (Don't Repeat Yourself):** Reusing everything possible is mandatory across the entire codebase.
  - **Frontend:** Never define one-off magic numbers for margins, paddings, gaps, font sizes, line heights, or colors. Reuse tokens from `web/src/styles/tokens.css` (e.g. `--shelf-pad-y`, `--gutter`, `--bar-h`) and shared CSS classes in `web/src/app.css` (e.g. `.btn`, `.btn.pill`, `.chip`, `.badge`, `.editorial-kicker`, `.editorial-title`, `.editorial-brief`). Never duplicate CSS declarations across twin elements or parallel sections—factor shared styling into single reusable classes or CSS variables.
  - **Backend:** Reuse shared helper functions, constants, and types rather than duplicating logic across handlers, stream managers, scanners, or probes.
- **Code style:** idiomatic, small packages, the standard library first, and comments only where the code isn't obvious. Match the surrounding code.
- **Specs** live in `docs/`: `DESIGN.md`, then one file per milestone (`M6-METADATA.md`, `M7-FOLDERS.md`, `M8-TVDB.md`, ...).
  Read the relevant one before changing that area, and keep it up to date.
