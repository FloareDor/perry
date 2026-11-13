#!/usr/bin/env sh
# Fetch the benchmark corpora. Idempotent: repositories already present are
# left alone, so this is safe to re-run.
#
#   sh scripts/corpora.sh
#
# Three real repositories, cloned bare, about 50 MB in total.
set -eu

cd "$(dirname "$0")/.."
mkdir -p testdata

# fetch <dir> <label> <url> [extra git clone args...]
fetch() {
	dir="testdata/$1"
	label="$2"
	url="$3"
	shift 3
	if [ -f "$dir/HEAD" ]; then
		printf '  %-10s already present\n' "$label"
		return
	fi
	printf '  %-10s cloning %s\n' "$label" "$url"
	git clone --quiet --bare "$@" "$url" "$dir"
}

echo "fetching benchmark corpora into testdata/"
fetch cobra.git      cobra-200 https://github.com/spf13/cobra --depth 200
fetch cobra-full.git cobra     https://github.com/spf13/cobra
fetch grpc-go.git    grpc-go   https://github.com/grpc/grpc-go
echo
echo "done. now run:  make bench"
