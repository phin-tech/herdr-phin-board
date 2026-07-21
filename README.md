# herdr-phin-board

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
   herdr-board            ~/src/github.com/phin-tech/herdr-phin-board                      ·idle
❯▾ Waiting (2)
   api-gateway            waiting on Dave re: API key                                 ·idle
   docs-site              vendor SLA response, chased 2026-07-18                   ·blocked
 ▸ Done (0)

 1-9 status · s pick · n note · enter jump · a archive · S statuses · ? help
```

Statuses are yours: rename them, reorder them, invent new ones. The dim right
column is Herdr's agent state — a hint only. It never groups, sorts, or
overrides anything you set.

`K` swaps the same board into kanban columns, one per status:

```
 Board                                                                          5 live · archive hidden

 Todo 0                   In Progress 2            Waiting 2                Done 1
 ──────────────────────── ──────────────────────── ──────────────────────── ────────────────────────
 —                          dev-stream             ❯ api-gateway              billing
                            ·idle                    waiting on Dave re:      ·idle
                                                     API key rotation
                            herdr-board              ·idle
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

## Install

```sh
git clone https://github.com/phin-tech/herdr-phin-board
herdr plugin link ./herdr-phin-board
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

Requires Go on `PATH` (the plugin builds itself on install) and Herdr 0.7.4+.

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

## Keys

| Key | |
|---|---|
| `K` | toggle between the list and the kanban columns |
| `d` | list: show or hide the detail pane · kanban: open the detail modal |
| `j` / `k` | move |
| `h` / `l` | kanban: move between columns · list: collapse / expand a group |
| `v` | grab the row, then move it — leaving its group changes its status |
| `enter` | jump to the space (reopens archived ones at their old path) |
| `1`–`9` | send to that status; the numbers are listed along the bottom |
| `s` | status picker |
| `n` | edit the note — who or what you're waiting on |
| `space` | collapse / expand a group |
| `a` | show or hide archived spaces |
| `/` | filter by name, path, or note |
| `S` | manage statuses: add, rename, reorder, delete |
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
