#!/bin/bash
# Regenerate ZPT test fixtures from source files
# Usage: cd test && ./generate_fixtures.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES_DIR="$SCRIPT_DIR/fixtures"

cd "$FIXTURES_DIR"

echo "Generating test fixtures..."

# js-event.zpt
rm -f js-event.zpt
(cd js-event && zip -r -X ../js-event.zpt .)
echo "  Created js-event.zpt"

# js-event-timeout.zpt
rm -f js-event-timeout.zpt
(cd js-event-timeout && zip -r -X ../js-event-timeout.zpt .)
echo "  Created js-event-timeout.zpt"

# multi-resource.zpt
rm -f multi-resource.zpt
(cd multi-resource && zip -r -X ../multi-resource.zpt .)
echo "  Created multi-resource.zpt"

# multi-page.zpt
rm -f multi-page.zpt
(cd multi-page && zip -r -X ../multi-page.zpt .)
echo "  Created multi-page.zpt"

# missing-index.zpt
rm -f missing-index.zpt
(cd missing-index && zip -r -X ../missing-index.zpt .)
echo "  Created missing-index.zpt"

# test.zpt
rm -f test.zpt
zip -r -X test.zpt test.html subdirectory
echo "  Created test.zpt"

# corrupt.zpt is manually created (not a valid ZIP)
# It should already exist as raw bytes

# isolation-writer.zpt / isolation-reader.zpt
rm -f isolation-writer.zpt
(cd isolation-writer && zip -r -X ../isolation-writer.zpt .)
rm -f isolation-reader.zpt
(cd isolation-reader && zip -r -X ../isolation-reader.zpt .)
echo "  Created isolation-writer.zpt and isolation-reader.zpt"

echo "Done. All fixtures generated."
