#!/bin/sh

set -eu

conf_path_param="";

if [ "${EBK_SECRET_KEY:-}" != "" ]; then
    source_conf_path="${EBK_CONF_PATH:-/ezbookkeeping/conf/ezbookkeeping.ini}"
    runtime_conf_path="/tmp/ezbookkeeping.ini"

    awk -v secret_key="${EBK_SECRET_KEY}" '
        BEGIN { in_security = 0; replaced = 0 }
        /^\[security\][[:space:]]*$/ { in_security = 1 }
        /^\[/ && $0 !~ /^\[security\][[:space:]]*$/ { in_security = 0 }
        in_security && $0 ~ /^[[:space:]]*secret_key[[:space:]]*=/ {
            print "secret_key = " secret_key
            replaced = 1
            next
        }
        { print }
        END {
            if (!replaced) {
                print ""
                print "[security]"
                print "secret_key = " secret_key
            }
        }
    ' "${source_conf_path}" > "${runtime_conf_path}"
    conf_path_param="--conf-path=${runtime_conf_path}"
elif [ "${EBK_CONF_PATH:-}" != "" ]; then
    conf_path_param="--conf-path=${EBK_CONF_PATH}";
fi

if [ $# -gt 0 ]; then
    exec "$@"
else
    if [ -n "${conf_path_param}" ]; then
        exec /ezbookkeeping/ezbookkeeping server run "${conf_path_param}"
    fi
    exec /ezbookkeeping/ezbookkeeping server run
fi
