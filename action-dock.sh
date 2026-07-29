#!/bin/sh
# Opens the narrow board beside your panes, on the right.
#
# Two runtimes, one entrypoint. hoarder has a real right-hand dock -- a
# persistent region that survives layout changes -- and `dock` is how you ask
# for it. Upstream Herdr has no such region, so the board goes into a tiled
# split instead; the split's side is not a manifest field, which is why this is
# a script rather than a plain pane declaration.
set -eu

bin="${HERDR_BIN_PATH:-herdr}"
plugin="${HERDR_PLUGIN_ID:-phin-board}"

# Whether this runtime has a dock is settled by asking it to dock, not by
# probing for the command first. `herdr dock --help` on upstream prints the
# general help and exits 0 -- --help short-circuits before the unknown command
# is reached -- so a probe reports a dock that is not there and the real call
# then fails. Running it for real is the only honest test: upstream exits 2.
#
# Its output is held back until it has worked, so upstream's "unknown command"
# never reaches the terminal, and hoarder's pane JSON still reaches the log.
if docked=$("$bin" dock "$plugin" sidebar 2>/dev/null); then
  printf '%s\n' "$docked"
  exit 0
fi

# A fresh split is an even one, and half the window is far more than a board
# this narrow needs -- it is a strip you glance at, not a pane you work in.
#
# There is no width to ask for at open time: it is not a manifest field, and
# `plugin pane open` has no flag for it. So the split is opened and then
# narrowed, which needs no measuring: --amount is a fraction of the window, and
# a new split is always half of it, so taking a quarter away leaves a quarter.
"$bin" plugin pane open \
  --plugin "$plugin" \
  --entrypoint side \
  --placement split \
  --direction right \
  --focus

# The board is worth having at the wrong width, so a refusal here does not fail
# the action: an older Herdr may not take a fractional amount, and the board is
# already open and usable by this point.
"$bin" pane resize --current --direction right --amount 0.25 || true
