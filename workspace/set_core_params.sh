#!/bin/bash
echo 'net.core.rmem_max = 212992000' >> /etc/sysctl.conf
echo 'net.core.rmem_default = 212992000' >> /etc/sysctl.conf
echo 'net.core.wmem_max = 212992000' >> /etc/sysctl.conf
echo 'net.core.wmem_default = 212992000' >> /etc/sysctl.conf
echo 'net.ipv4.tcp_rmem = 4096000 131072000 629145600' >> /etc/sysctl.conf
echo 'net.ipv4.tcp_wmem = 4096000 16384000 419430400' >> /etc/sysctl.conf
for i in ens11np0 ens13np0 ens15np0 ens17np0; do
    echo "net.ipv4.conf.${i}.arp_ignore=2    " >> /etc/sysctl.conf
    echo "net.ipv4.conf.${i}.arp_announce=1  " >> /etc/sysctl.conf
    echo "net.ipv4.conf.${i}.rp_filter=2     " >> /etc/sysctl.conf
    echo "net.ipv6.conf.${i}.disable_ipv6=0  " >> /etc/sysctl.conf
done
sysctl -p
