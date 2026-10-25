#!/usr/bin/env bash
set -eu

# Local baseline: 2.11.4 <= version < 3.0.0. CI supplies an exact version.
output=${1:-}
exact=${2:-}
printf '%s\n' "$output"
if [[ ! $output =~ ^golangci-lint\ has\ version\ ([0-9]+)\.([0-9]+)\.([0-9]+)([[:space:]]|$) ]]; then
    echo 'Cannot parse a stable golangci-lint version.' >&2
    exit 1
fi
major=${BASH_REMATCH[1]}
minor=${BASH_REMATCH[2]}
patch=${BASH_REMATCH[3]}
version="$major.$minor.$patch"
if [[ -n $exact ]]; then
    if [[ $version != "$exact" ]]; then
        printf 'Expected golangci-lint %s, found %s.\n' "$exact" "$version" >&2
        exit 1
    fi
elif (( 10#$major != 2 || 10#$minor < 11 || (10#$minor == 11 && 10#$patch < 4) )); then
    printf 'Expected golangci-lint >=2.11.4 and <3.0.0, found %s.\n' "$version" >&2
    exit 1
fi
