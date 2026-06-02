#!/bin/bash
apt-get install linux-headers-$(uname -r)
export KERNELDIR=/usr/src/kernels/$(uname -r)
bash /home/wangxuanqi/shanghai/xre-Linux-x86_64-5.0.21.24.4.run
cat /proc/kunlun/version|grep KUNLUN|awk '{print $10}'
sleep 10
cd /home/wangxuanqi/shanghai
tar -xzf xdr_copy-x86_64_1.1.0.6.tar.gz
cd /home/wangxuanqi/shanghai/xdr_copy-x86_64_1.1.0.6
export KERNELDIR=/usr/src/linux-headers-$(uname -r)
./build.sh
rm -rf /lib/modules/`uname -r`/extra/xdr.ko && rmmod xdr
sudo depmod && sudo dracut -f
./install.sh
cat /proc/xdr/version
dmesg -T | grep 'XDR disabled'
cd /home/wangxuanqi/shanghai
tar -zxvf /home/wangxuanqi/shanghai/update_fw_p800_2.15_1.48.tar.gz >>/dev/null && cd update_fw && bash auto_update.sh
apt install ipmitool -y
ipmitool power cycle
