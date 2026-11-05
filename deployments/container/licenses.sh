#!/bin/sh
# Runs only in the native Go build stage; target settings select analyzed imports.
set -eu

mkdir -p /out/licenses/project /out/licenses/go /out/licenses/upstream
cp LICENSE /out/licenses/project/LICENSE
cp /usr/local/go/LICENSE /out/licenses/go/LICENSE
if [ -f /usr/local/go/PATENTS ]; then cp /usr/local/go/PATENTS /out/licenses/go/PATENTS; fi
/tools/go-licenses save --ignore github.com/open-agentic-catalog/open-agentic-catalog \
    --save_path /out/licenses/dependencies ./cmd/catalogd ./cmd/catalogctl
# The entire pinned upstream tree is embedded. Preserve existing attribution paths.
find schemas/upstream -type f \( -iname 'license*' -o -iname 'notice*' -o -iname 'copying*' -o -path '*/LICENSES/*' \) -exec sh -eu -c '
    for source do
        destination="/out/licenses/upstream/${source#schemas/upstream/}"
        mkdir -p "${destination%/*}"
        cp "$source" "$destination"
    done
' sh {} +
cp schemas/checksums.txt /out/licenses/upstream/checksums.txt
# Samples are embedded too. Preserve their existing notices, not the licenses
# of resource runtimes that the sample metadata merely describes.
find samples -type f \( -iname 'license*' -o -iname 'notice*' -o -iname 'copying*' -o -path '*/LICENSES/*' -o -path 'samples/artwork/README.md' \) -exec sh -eu -c '
    for source do
        destination="/out/licenses/$source"
        mkdir -p "${destination%/*}"
        cp "$source" "$destination"
    done
' sh {} +
