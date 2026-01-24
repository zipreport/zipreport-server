#!/bin/bash
# Regenerate ZPT test fixtures from source files
# Usage: cd test && ./generate_fixtures.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES_DIR="$SCRIPT_DIR/fixtures"

cd "$FIXTURES_DIR"

echo "Generating test fixtures..."

# js-event.zpt
(cd js-event && zip -r ../js-event.zpt .)
echo "  Created js-event.zpt"

# js-event-timeout.zpt
(cd js-event-timeout && zip -r ../js-event-timeout.zpt .)
echo "  Created js-event-timeout.zpt"

# multi-resource.zpt
(cd multi-resource && zip -r ../multi-resource.zpt .)
echo "  Created multi-resource.zpt"

# multi-page.zpt
(cd multi-page && zip -r ../multi-page.zpt .)
echo "  Created multi-page.zpt"

# missing-index.zpt
(cd missing-index && zip -r ../missing-index.zpt .)
echo "  Created missing-index.zpt"

# corrupt.zpt is manually created (not a valid ZIP)
# It should already exist as raw bytes

echo "Done. All fixtures generated."
