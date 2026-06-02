#!/bin/bash
set -euo pipefail

if [ "$#" -ne 4 ]; then
    echo "Usage: $0 <ens11np0_ip> <ens13np0_ip> <ens15np0_ip> <ens17np0_ip>"
    echo "Example: $0 11.1.1.11 11.1.2.11 11.1.3.11 11.1.4.11"
    exit 1
fi

NETPLAN_DIR="/etc/netplan"
CIDR="24"

update_ip() {
    local file="$1"
    local new_ip="$2"

    if [ ! -f "$file" ]; then
        echo "Error: file not found: $file"
        exit 1
    fi

    cp -a "$file" "${file}.bak.$(date +%Y%m%d%H%M%S)"

    python3 - "$file" "$new_ip" "$CIDR" <<'PY'
import sys
import re

file_path = sys.argv[1]
ip = sys.argv[2]
cidr = sys.argv[3]

with open(file_path, "r", encoding="utf-8") as f:
    text = f.read()

pattern = r'(^\s*-\s*)(\d+\.\d+\.\d+\.\d+/\d+)(\s*$)'
replacement = r'\g<1>' + f'{ip}/{cidr}' + r'\g<3>'

new_text, count = re.subn(pattern, replacement, text, count=1, flags=re.M)

if count != 1:
    print(f"Error: failed to update addresses entry in {file_path}", file=sys.stderr)
    sys.exit(1)

with open(file_path, "w", encoding="utf-8") as f:
    f.write(new_text)

print(f"Updated {file_path} -> {ip}/{cidr}")
PY
}

update_ip "${NETPLAN_DIR}/ens11np0.yaml" "$1"
update_ip "${NETPLAN_DIR}/ens13np0.yaml" "$2"
update_ip "${NETPLAN_DIR}/ens15np0.yaml" "$3"
update_ip "${NETPLAN_DIR}/ens17np0.yaml" "$4"

echo "All files updated successfully."
echo "Please review with: cat /etc/netplan/ens*.yaml"
echo "Then apply with: netplan generate && netplan apply"
