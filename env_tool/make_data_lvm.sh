#!/bin/bash
set -euo pipefail

DISK1="/dev/nvme0n1"
DISK2="/dev/nvme1n1"
VG_NAME="vg_data"
LV_NAME="lv_data"
MOUNT_POINT="/data"
FS_TYPE="ext4"

echo "The following disks will be used to create the LVM volume:"
lsblk -f "$DISK1" "$DISK2"

echo
echo "WARNING: This operation will erase all data on $DISK1 and $DISK2."
read -rp "Type YES to continue: " confirm
[ "$confirm" = "YES" ] || exit 1

sudo wipefs -a "$DISK1" "$DISK2"

sudo pvcreate "$DISK1" "$DISK2"
sudo vgcreate "$VG_NAME" "$DISK1" "$DISK2"
sudo lvcreate -n "$LV_NAME" -l 100%FREE "$VG_NAME"

sudo mkfs.ext4 "/dev/$VG_NAME/$LV_NAME"

sudo mkdir -p "$MOUNT_POINT"

UUID=$(sudo blkid -s UUID -o value "/dev/$VG_NAME/$LV_NAME")

sudo cp /etc/fstab /etc/fstab.bak.$(date +%F-%H%M%S)

sudo sed -i "\|[[:space:]]$MOUNT_POINT[[:space:]]|d" /etc/fstab
echo "UUID=$UUID $MOUNT_POINT $FS_TYPE defaults 0 2" | sudo tee -a /etc/fstab

sudo mount -a
df -Th "$MOUNT_POINT"

echo
echo "LVM volume has been created and mounted successfully."
echo "Volume path: /dev/$VG_NAME/$LV_NAME"
echo "Mount point: $MOUNT_POINT"