# herdr-phin-board

[![ci](https://github.com/phin-tech/herdr-phin-board/actions/workflows/ci.yml/badge.svg)](https://github.com/phin-tech/herdr-phin-board/actions/workflows/ci.yml)

A [Herdr](https://herdr.dev) plugin: a status board over your spaces, in a popup,
on a key.

Herdr's own space list tells you what Herdr knows — label, panes, agent state.
This adds the part only you know: what you've actually started, what's finished,
and what's parked because you're waiting on a person or something outside the
machine.

```
 Board                                                              4 live · archive hidden

 ▾ Todo (0)
 ▾ In Progress (2)
   dev-stream             ~/src/github.com/phin-tech/dev-stream                    ·working
   herdr-phin-board       ~/src/github.com/phin-tech/herdr-phin-board                 ·idle
❯▾ Waiting (2)
   api-gateway            waiting on Dave re: API key                                 ·idle
   docs-site              vendor SLA response, chased 2026-07-18                   ·blocked
 ▸ Done (0)

 1-9 status · s pick · n note · enter jump · a archive · S statuses · ? help
```

Statuses are yours: rename them, reorder them, invent new ones. The dim right
column is Herdr's agent state — a hint only. It never groups, sorts, or
overrides anything you set.

`K` cycles the same board through three views. The **table** is the flat one —
every space on a line, in aligned columns, including the fields the list has no
room for:

```
 Board                                                                            5 live · archive hidden
   SPACE                ↓STATUS     NOTE                                              AGENT    CHANGED
   dev-stream           In Progress —                                                 idle     just now
   herdr-phin-board     In Progress —                                                 working  2h ago
 ❯ api-gateway          Waiting     waiting on Dave re: API key rotation — he's back  idle     10m ago
   docs-site            Waiting     vendor SLA response, chased 2026-07-18            blocked  1d ago
   billing              Done        —                                                 idle     3d ago
```

No groups, no collapse, and it's the only view you can re-sort: `o` cycles
status → name → changed, and `↓` marks the column in force. Sorting by status
lays the rows out exactly as the list groups them, so `v` still works there.

The **kanban** is columns, one per status:

```
 Board                                                                          5 live · archive hidden

 Todo 0                   In Progress 2            Waiting 2                Done 1
 ──────────────────────── ──────────────────────── ──────────────────────── ────────────────────────
 —                          dev-stream             ❯ api-gateway              billing
                            ·idle                    waiting on Dave re:      ·idle
                                                     API key rotation
                            herdr-phin-board         ·idle
                            ·working
                                                     docs-site
                                                     vendor SLA response,
                                                     chased 2026-07-18
                                                     ·blocked
```

A column *is* a status, so `v` then `h`/`l` walks a card sideways to retag it,
and `j`/`k` reorders within the column. The view you were last in is remembered.

Rows can only ever show a truncated note, so the list keeps a detail pane
alongside. It tracks the cursor with no keypress needed, and shows the note in
full along with the path, workspace, and when the status last changed. `d` hides
it if you want the room back. In kanban the columns already use the width, so
`d` opens the same detail as a modal instead — and you can keep browsing with
`j`/`k`, or edit with `n`, without closing it.

## Pull request context

If a space's directory has a pull request for its current branch, the board
shows it: number, state, review decision and CI checks. Worktrees are the case
this suits best — one branch per space, so one PR per row.

```
   SPACE                STATUS      NOTE                          PR                   AGENT
   billing              In Progress —                             #130 ● changes ·     working
   docs-site            Waiting     —                             #119 ○ ✗             blocked
 ❯ api-gateway          Waiting     waiting on Dave re: API key   #123 ● approved ✓    idle
```

`●` open · `○` draft · `◆` merged · `✕` closed, then the review decision, then
checks `✓` pass `✗` fail `·` running, then `conflict` or `behind` when the
branch cannot land as it stands. A mergeable branch says nothing — the column
is for what needs doing, and GitHub computes mergeability lazily, so an
un-computed state is left blank rather than guessed. A row is coloured by whatever most needs
attention: failing checks first, then changes-requested. `gp` opens the
selected space's PR in a browser.

**PR state is context, never control.** It never sets a status, moves a row, or
reorders anything — the same rule the agent hint follows. You drive status; this
just tells you what GitHub thinks while you decide.

## Talking to a space's agent

`m` types a message into the agent running in the selected space and takes you
there — **without submitting it**. You read it, add a line, press enter. The
board is where you notice that something is blocked; this is how you say
something about it without hunting for the right window.

It refuses rather than guesses. Panes with no agent are skipped (shells, plugin
sidebars, this board), and a space running two agents is a refusal, not a coin
flip: typing a review comment into the wrong agent is worse than not sending it.

The PR is found by the space's branch, or by a URL an agent printed: the board
reads recent pane output for a `…/pull/N` link, which catches a pull request the
moment it is announced and reaches ones a branch lookup cannot. It reads through
the `gh` CLI, so it uses your existing login and needs no token. Results are cached beside the board and refreshed when you open it, so
the board paints instantly and fills in as answers arrive. No `gh`, no repo, no
No repo and no PR both look the same — an empty column. A **missing or
logged-out `gh` says so once**, because Herdr launches plugins with a minimal
PATH: left silent, the whole feature would vanish and look identical to having
no PRs.

The short form is also pushed as a `$pr` token, if you want it in the sidebar:

```toml
[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  ["branch", "$status"],
  ["$pr"],
]
```

## Notifications and the bell

A watcher polls your spaces' pull requests in the background, every two minutes,
and raises a Herdr notification when something actually changes:

| | |
|---|---|
| checks pass → fail | the thing you were waiting on just broke |
| a review lands | approved, or changes requested |
| clean → conflict | needs a rebase before it can land |
| merged or closed | the work landed, or didn't |

**Only changes, never states.** A failing check is announced once, not on every
poll — notifying on state trains you to ignore the notifications.

Herdr toasts are transient: fired while you are away, they are gone. So every
notification is also recorded, and the space wears a 🔔 until you look at it.
Selecting the row clears it; the count in the header means a bell inside a
collapsed group or an off-screen column is still visible. The detail view spells
out what happened, and names the checks that are failing rather than just saying
some are.

The watcher starts itself when you open the board and keeps running after you
close it. It holds an event subscription, which does double duty: Herdr closing
drops the connection so the watcher exits at once rather than discovering the
loss on its next tick, and a workspace appearing or closing nudges it to poll
then instead of waiting out the timer. A burst of events still costs one poll.

A lockfile means there is only ever one watcher, however many times you open the
board, and it covers every space across every repo. Run it by hand with
`herdr-phin-board watch` if you would rather.

It follows the Herdr session that started it. If you run named sessions
side by side, spaces in the other one are outside its view.

## Install

```sh
herdr plugin install phin-tech/herdr-phin-board
```

That runs the build step, which compiles from source if Go is on your `PATH`
and otherwise downloads the binary CI publishes, checking it against the
published `.sha256`. Either way you end up with `bin/herdr-phin-board`.

For local development, point Herdr at a working tree instead — no build runs,
so you compile it yourself:

```sh
git clone https://github.com/phin-tech/herdr-phin-board
cd herdr-phin-board && go build -o bin/herdr-phin-board ./cmd/herdr-phin-board
herdr plugin link .
```

Then bind a key in `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+d"
type = "plugin_action"
command = "phin-board.open"
description = "Space board"
```

and reload: `herdr server reload-config`.

`prefix+d` is unbound in Herdr's default keymap. A `keys.command` entry silently
shadows a built-in, so check the config reference before picking another —
`prefix+b` is `toggle_sidebar` and `prefix+k` is `focus_pane_up`, both easy to
lose by accident.

Requires Herdr 0.7.4+. Go is optional: without it the install falls back to a
prebuilt macOS or Linux binary.

## Status in the Spaces sidebar

The board mirrors each status into the workspace's `status` metadata token, so
it can show in Herdr's native Spaces sidebar too. Add `$status` to a row:

```toml
[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  ["$status"],
]
```

Tokens don't survive a server restart, but the board file does — a
`workspace.created` hook re-applies the stored status whenever a space appears,
so the badge is correct even if you never open the board.

**The default status gets no badge.** Every space you have never touched sits
there, so badging it would put the same word on every sidebar row while telling
you nothing — and it could not distinguish "I filed this as Todo" from "I have
never looked at this". That is why the shipped set leads with **Triage**: it
means *not looked at yet*, which leaves Todo free to mean a decision you
actually made, badge and all.

Which status is the default is an explicit choice, not a position: press `D` on
one in the `S` manager. It is marked there, and it can sit anywhere in the
order, so rearranging the board never silently changes which status goes quiet.
A board that has never named one falls back to the first.

## Keys

| Key | |
|---|---|
| `K` | cycle the view: list → table → kanban (or click the title) |
| `o` | table only: sort by status, name, or when it last changed |
| `d` | list: show or hide the detail pane · elsewhere: detail modal |
| `j` / `k` | move |
| `gg` / `G` | first row · last row |
| `gp` | open the pull request in a browser |
| `h` / `l` | kanban: move between columns · list: collapse / expand a group |
| `v` | grab the row, then move it — leaving its group changes its status |
| `enter` | jump to the space (reopens archived ones at their old path) |
| `1`–`9` | send to that status; the numbers are listed along the bottom |
| `s` | status picker |
| `n` | edit the note — who or what you're waiting on |
| `R` | rename the space — renames the Herdr workspace too |
| `m` | type a message into that space's agent, then go there to send it |
| `space` | collapse / expand a group |
| `a` | show or hide archived spaces |
| `/` | filter by name, path, or note |
| `S` | manage statuses: add, rename, reorder, delete, set the default |
| `x` | forget the selected space |
| `r` | refresh |
| `q` | quit |

## How state works

Herdr workspace ids (`w4`, `w5`) are reassigned every session, so they're
useless as a durable key. Statuses are keyed by **canonical directory path**
instead: a status set today is still on the project when you reopen it next
week, in a new session, with a different workspace id.

Two axes are deliberately kept apart:

- **Status** is yours, and it groups the board. A live space marked Done sits in
  the Done group.
- **Liveness** is Herdr's, and it decides main list vs archive. Close a space in
  Herdr and it moves behind `a` with its status intact; `enter` reopens it.

Everything lives in one file, `$HERDR_PLUGIN_STATE_DIR/board.json` — status
definitions and per-directory entries together, written atomically. It's
hand-editable if you'd rather.

Rows sort by most-recently-touched until you arrange a column by hand with `v`.
After that the arrangement sticks: hand-ranked rows hold their positions at the
top of the group, and anything you haven't touched falls in below them by
recency. Rearranging a row doesn't count as working on it, so it won't disturb
that fallback ordering.

Several workspaces open on the same directory share one row, because a status
belongs to the project rather than the window.

```sh
herdr-phin-board sync    # re-apply stored statuses to workspace tokens
herdr-phin-board watch   # poll PRs and notify (started automatically by the board)
herdr-phin-board prune   # forget entries whose directory no longer exists
```

## Development

```sh
go test ./...
go build -o bin/herdr-phin-board ./cmd/herdr-phin-board
./bin/herdr-phin-board          # runs against the live session via $HERDR_SOCKET_PATH
```

Run the binary directly from any pane inside a Herdr session — it doesn't need
to be installed as a plugin to work, which makes for a fast inner loop.
