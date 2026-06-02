#!/bin/bash
set -euo pipefail

DIR="/etc/networkd-dispatcher/routable.d"

for f in "$DIR"/config_rt_ens*.sh; do
    [ -f "$f" ] || continue

    DEV="$(basename "$f" | sed -E 's/^config_rt_(.*)\.sh$/\1/')"

    IP="$(ip -4 addr show "$DEV" 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1 | head -n1)"

    if [ -z "$IP" ]; then
        echo "[WARN] $DEV has no IPv4 address, skip $f"
        continue
    fi

    GW="$(awk -F. '{print $1"."$2"."$3".1"}' <<< "$IP")"

    # 按网卡名映射 table
    case "$DEV" in
        ens11np0) TABLE="101" ;;
        ens13np0) TABLE="102" ;;
        ens15np0) TABLE="103" ;;
        ens17np0) TABLE="104" ;;
        *)
            echo "[WARN] unknown device $DEV, skip $f"
            continue
            ;;
    esac

    BAK="${f}.bak.$(date +%Y%m%d_%H%M%S)"
    cp "$f" "$BAK"

    sed -i -E \
        -e "s/^IP=\"[^\"]*\"$/IP=\"${IP}\"/" \
        -e "s/^DEV=\"[^\"]*\"$/DEV=\"${DEV}\"/" \
        -e "s/^TABLE=\"[^\"]*\"$/TABLE=\"${TABLE}\"/" \
        -e "s/^GW=\"[^\"]*\"$/GW=\"${GW}\"/" \
        "$f"

    echo "[OK] updated $f"
    echo "     DEV=$DEV IP=$IP GW=$GW TABLE=$TABLE"
done
