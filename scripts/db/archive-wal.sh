#!/bin/sh
# One archiver writes to this lab's private repository. Use a backup manager for production.
set -eu
source_file=$1
destination=/backup/wal/$2
if [ -f "$destination" ]; then
    cmp -s "$source_file" "$destination"
    sync
    exit 0
fi
cp "$source_file" "$destination.partial"
sync
mv "$destination.partial" "$destination"
sync
