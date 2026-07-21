#!/bin/sh
# Opens the board popup. Unlike per-project plugins, the board is global -- it
# lists every space -- so there is no cwd to resolve from the invocation context.
set -eu

exec "${HERDR_BIN_PATH:-herdr}" plugin pane open \
  --plugin "${HERDR_PLUGIN_ID:-phin-board}" \
  --entrypoint phin-board
