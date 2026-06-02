#!/bin/bash

IP="11.1.1.11"
DEV="ens11np0"
TABLE="101"
GW="11.1.1.1"

# ensure ip
if ! ip addr show $DEV | grep -q "inet "; then
    echo "device $DEV no ip address, so add ip address for it"
    ip addr add ${IP}/24 brd 11.1.1.255 dev $DEV
fi

# clean old rules
ip rule show | grep $DEV | while read line; do
    ip rule del ${line#*: } 2>/dev/null
done

# add route
ip route replace default via $GW dev $DEV table $TABLE
ip route replace 11.1.0.0/21 via $GW table $TABLE src $IP proto static

# add rules
ip rule add from all oif $DEV table $TABLE priority 32761
ip rule add from $IP table $TABLE priority 32761
