#!/bin/sh
cd "$(dirname "$0")" || exit 1
xattr -d com.apple.quarantine ./imagecaster-server 2>/dev/null
exec ./imagecaster-server
