#!/usr/bin/env bash
set -eu
checker="$(dirname "$0")/check-lint-version.sh"

check() {
    local expected=$1 version=$2 exact=${3:-} result=0
    bash "$checker" "golangci-lint has version $version built with go1.26.2" "$exact" >/dev/null 2>&1 || result=$?
    if [[ $expected == pass && $result != 0 || $expected == fail && $result == 0 ]]; then
        printf 'FAIL: version=%s exact=%s expected=%s exit=%s\n' "$version" "$exact" "$expected" "$result" >&2
        exit 1
    fi
}

check fail 2.10.9
check fail 2.11.3
check pass 2.11.4
check pass 2.11.5
check pass 2.14.0
check pass 2.100.0
check fail 1.99.0
check fail 3.0.0
check fail 2.14.0-rc1
check fail dev
check pass 2.11.4 2.11.4
check fail 2.14.0 2.11.4
check fail 2.11.3 2.11.4
if bash "$checker" '' >/dev/null 2>&1; then
    echo 'FAIL: empty version output accepted' >&2
    exit 1
fi
echo 'lint version checks: PASS'
