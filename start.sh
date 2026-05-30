#!/bin/sh
# Starts Litestream replication in the background, then runs the Hearth server.
# If the server exits for any reason, Litestream is also killed.
set -e

# Restore from the latest Litestream snapshot before first boot (no-op if DB
# already exists on the volume — e.g. on a normal restart).
if [ ! -f "$HEARTH_DB_PATH" ]; then
  echo "No database found — attempting Litestream restore..."
  litestream restore -config /app/litestream.yml -if-replica-exists "$HEARTH_DB_PATH" || true
fi

# Start Litestream replication as a background process.
litestream replicate -config /app/litestream.yml &
LITESTREAM_PID=$!

# Run the server.
/app/hearth

# If hearth exits, also kill Litestream.
kill "$LITESTREAM_PID" 2>/dev/null || true
