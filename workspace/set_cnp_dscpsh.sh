#!/usr/bin/env bash
set -euo pipefail

TARGET_DSCP=48
KEY="CNP_DSCP_P1"

echo "== CX7 (mt4129) CNP DSCP batch config =="
echo "Target: ${KEY} = ${TARGET_DSCP}"
echo

found=0

for dev in /dev/mst/mt4129_pciconf*; do
    [[ -e "$dev" ]] || continue
    [[ "$dev" =~ \.[0-9]+$ ]] && continue

    found=1
    echo ">>> Processing $dev"

    current=$(
        mlxconfig -d "$dev" query \
        | awk -v k="$KEY" '$1==k {print $NF}'
    )

    if [[ -z "$current" ]]; then
        echo "    [ERROR] $KEY not found on this device"
        continue
    fi

    echo "    Current $KEY = $current"

    if [[ "$current" == "$TARGET_DSCP" ]]; then
        echo "    [OK] Already set, skip"
        echo
        continue
    fi

    echo "    Setting $KEY=$TARGET_DSCP"
    mlxconfig -d "$dev" set "$KEY=$TARGET_DSCP"

    echo "    [DONE] Config written (reboot required)"
    echo
done

if [[ "$found" -eq 0 ]]; then
    echo "[ERROR] No CX7 (mt4129) devices found"
    exit 1
fi

echo "== All CX7 devices processed =="
echo "!! Reboot is REQUIRED for changes to take effect !!"
