#!/bin/sh

iclist=ic1
ic1='tp_shell tp_utilities'
tet_startup=startup
tet_cleanup=cleanup
startup() {
 work=$(mktemp -d "${POSIX_OUT:?}/tet-work.XXXXXX") || exit 1
}
cleanup() { rm -rf -- "$work"; }
tp_shell() {
 value=$("${SANDBOX_CLI:?}" bash --module "${WASM_MODULE:?}" --dir "$work" -c 'f() { printf "%s" "$1"; }; f "a b"')
 status=$?
 tet_infoline "assistant-wasi shell: exit=$status stdout=$value"
 if [ "$status" -ne 0 ]; then tet_result UNRESOLVED
 elif [ "$value" = 'a b' ]; then tet_result PASS
 else tet_result FAIL
 fi
}
tp_utilities() {
 value=$("${SANDBOX_CLI:?}" bash --module "${WASM_MODULE:?}" --dir "$work" -c 'printf "a\nb\n" | wc -l')
 status=$?
 tet_infoline "assistant-wasi utilities: exit=$status stdout=$value"
 if [ "$status" -ne 0 ]; then tet_result UNRESOLVED
 elif [ "$value" = 2 ]; then tet_result PASS
 else tet_result FAIL
 fi
}
. "${TET_ROOT:?}/lib/posix_sh/tcm.sh"
