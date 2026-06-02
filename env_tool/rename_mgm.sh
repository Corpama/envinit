#!/usr/bin/env bash
set -euo pipefail

# Usage:
#   sudo bash rename_nic_by_mac.sh <current_nic_name> <new_nic_name>
#
# Example:
#   sudo bash rename_nic_by_mac.sh ens160 eth0

RULE_FILE="/etc/udev/rules.d/70-persistent-net.rules"

usage() {
  echo "Usage: sudo bash $0 <current_nic_name> <new_nic_name>"
  echo "Example: sudo bash $0 ens160 eth0"
  exit 1
}

if [[ $# -ne 2 ]]; then
  usage
fi

OLD_IF="$1"
NEW_IF="$2"

if [[ $EUID -ne 0 ]]; then
  echo "Please run as root, for example: sudo bash $0 $OLD_IF $NEW_IF"
  exit 1
fi

if [[ ! -d "/sys/class/net/${OLD_IF}" ]]; then
  echo "Error: interface ${OLD_IF} does not exist"
  exit 1
fi

if [[ ${#NEW_IF} -gt 15 ]]; then
  echo "Error: interface name must not exceed 15 characters"
  exit 1
fi

if [[ -d "/sys/class/net/${NEW_IF}" ]]; then
  echo "Error: target interface name ${NEW_IF} already exists"
  exit 1
fi

MAC="$(cat /sys/class/net/${OLD_IF}/address | tr '[:upper:]' '[:lower:]')"

if [[ -z "${MAC}" ]]; then
  echo "Error: failed to read MAC address of ${OLD_IF}"
  exit 1
fi

echo "Detected interface information:"
echo "  Current interface : ${OLD_IF}"
echo "  MAC address       : ${MAC}"
echo "  Target name       : ${NEW_IF}"
echo

if [[ -f "${RULE_FILE}" ]]; then
  BACKUP="${RULE_FILE}.$(date +%F_%H%M%S).bak"
  cp -a "${RULE_FILE}" "${BACKUP}"
  echo "Backup created: ${BACKUP}"
fi

TMP_FILE="$(mktemp)"

if [[ -f "${RULE_FILE}" ]]; then
  grep -viE "ATTR\\{address\\}==\"${MAC}\"|NAME=\"${NEW_IF}\"" "${RULE_FILE}" > "${TMP_FILE}" || true
else
  : > "${TMP_FILE}"
fi

cat >> "${TMP_FILE}" <<EOF
SUBSYSTEM=="net", ACTION=="add", DRIVERS=="?*", ATTR{address}=="${MAC}", NAME="${NEW_IF}"
EOF

install -m 0644 "${TMP_FILE}" "${RULE_FILE}"
rm -f "${TMP_FILE}"

echo "udev rule has been written to:"
echo "  ${RULE_FILE}"
echo
cat "${RULE_FILE}"
echo

echo "Reloading udev rules..."
udevadm control --reload-rules

echo "Triggering udev for network devices..."
udevadm trigger --subsystem-match=net || true

if command -v update-initramfs >/dev/null 2>&1; then
  echo "Updating initramfs..."
  update-initramfs -u || true
fi

echo
echo "Done."
echo "Please reboot the system for the new interface name to take effect."
