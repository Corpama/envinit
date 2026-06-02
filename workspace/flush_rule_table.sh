#!/bin/bash
for t in 101 102 103 104; do
    ip route flush table $t
done

for t in 101 102 103 104; do
    ip rule flush table $t
done
