#!/usr/bin/env bash
       
cd `dirname $0`
default_grub_arg="/etc/default/grub"
default_grub_file="/boot/grub2/grub.cfg"
efi_grub_file="/boot/efi/EFI/kylin/grub.cfg"

sed -ri "s/(GRUB_CMDLINE_LINUX.*)(quiet)(.*)/\1\3/g" ${default_grub_arg}
#sed -ri "s/(GRUB_CMDLINE_LINUX.*)(crashkernel=auto)(.*)/\1\3/g" ${default_grub_arg}
# crashkernel=auto 
# -- default (/etc/default/grub)
# -- reserve memory for kernel crash automatically)
# -- systemctl enable kdump
sed -ri "s/(GRUB_CMDLINE_LINUX.*)(\")/\1 rw biosdevname=0 iommu=pt mitigations=off\2/g" ${default_grub_arg}
update-grub
