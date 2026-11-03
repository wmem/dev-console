#!/bin/bash
# 自解压入口：不联网，不执行解压内容以外的安装脚本。
set -euo pipefail
umask 077
if [[ "$(uname -s)/$(uname -m)" != 'Linux/x86_64' ]]; then
    echo '安装包仅支持Linux/amd64（x86_64）' >&2
    exit 1
fi
payload_line=@PAYLOAD_LINE@
if [[ "${1:-}" == '--extract' ]]; then
    if [[ $# -ne 2 ]]; then echo '用法：安装包 --extract <不存在的目录>' >&2; exit 2; fi
    mkdir -- "$2"
    if ! tail -n +"$payload_line" -- "$0" | gzip -dc > "$2/dconsole"; then
        rm -f -- "$2/dconsole"
        rmdir -- "$2"
        echo '安装包损坏，解压失败' >&2
        exit 1
    fi
    chmod 0755 -- "$2/dconsole"
    echo "已解压到 $2/dconsole；未执行安装"
    exit 0
fi
work=$(mktemp -d "${TMPDIR:-/tmp}/dconsole-install.XXXXXXXX")
trap 'rm -rf -- "$work"' EXIT
tail -n +"$payload_line" -- "$0" | gzip -dc > "$work/dconsole"
chmod 0700 -- "$work/dconsole"
if [[ "${1:-}" == '--version' ]]; then
    "$work/dconsole" version
else
    "$work/dconsole" install "$@"
fi
exit 0
# shellcheck disable=SC2317
__DCONSOLE_PAYLOAD__
