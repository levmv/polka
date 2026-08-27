#!/usr/bin/env python3
"""Wait until every browser-test server is accepting HTTP requests."""

import sys
import time
import urllib.error
import urllib.request


def main() -> None:
    pending = set(sys.argv[1:])
    deadline = time.monotonic() + 10
    while pending and time.monotonic() < deadline:
        for url in tuple(pending):
            try:
                with urllib.request.urlopen(url, timeout=0.2):
                    pending.remove(url)
            except (OSError, urllib.error.URLError):
                pass
        if pending:
            time.sleep(0.05)
    if pending:
        raise SystemExit(f"servers did not become ready: {', '.join(sorted(pending))}")


if __name__ == "__main__":
    main()
