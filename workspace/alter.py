#!/usr/bin/env python3
import json
import os
import re
import shutil
import subprocess
import sys
import time

NETPLAN_DIR = "/etc/netplan"
ROUTABLE_DIR = "/etc/networkd-dispatcher/routable.d"
IFACES = ["ens11np0", "ens13np0", "ens15np0", "ens17np0"]


def run_cmd(cmd):
    return subprocess.run(cmd, capture_output=True, text=True, check=False)


def get_iface_ipv4_info(iface):
    """
    Returns:
      ip: 11.1.1.11
      prefix: 24
      cidr: 11.1.1.11/24
      brd: 11.1.1.255
    """
    res = run_cmd(["ip", "-j", "-4", "addr", "show", "dev", iface])
    if res.returncode != 0:
        raise RuntimeError(f"Failed to get information for interface {iface}: {res.stderr.strip()}")

    try:
        data = json.loads(res.stdout)
    except Exception as e:
        raise RuntimeError(f"Failed to parse output from 'ip -j': {e}")

    if not data:
        raise RuntimeError(f"Interface {iface} does not exist or returned no output")

    addr_info = data[0].get("addr_info", [])
    for item in addr_info:
        if item.get("family") == "inet" and item.get("scope") == "global":
            ip = item.get("local")
            prefix = item.get("prefixlen")
            brd = item.get("broadcast")
            if not ip or prefix is None:
                continue
            return {
                "ip": ip,
                "prefix": prefix,
                "cidr": f"{ip}/{prefix}",
                "brd": brd if brd else "",
            }

    raise RuntimeError(f"No global IPv4 address found on interface {iface}")


def backup_file(path):
    ts = time.strftime("%Y%m%d_%H%M%S")
    bak = f"{path}.bak.{ts}"
    shutil.copy2(path, bak)
    return bak


def update_netplan_yaml(file_path, cidr):
    """
    Only update the first '- xxx/yy' entry under addresses:
    Keep original indentation and most of the existing formatting unchanged.
    """
    with open(file_path, "r", encoding="utf-8") as f:
        lines = f.readlines()

    changed = False
    for i, line in enumerate(lines):
        if re.match(r'^\s*addresses:\s*$', line):
            for j in range(i + 1, len(lines)):
                if re.match(r'^\s*-\s+\S+', lines[j]):
                    m = re.match(r'^(\s*)-\s+\S+', lines[j])
                    indent = m.group(1)
                    new_line = f"{indent}- {cidr}\n"
                    if lines[j] != new_line:
                        lines[j] = new_line
                        changed = True
                    break
            break

    if not changed:
        return False

    backup_file(file_path)
    with open(file_path, "w", encoding="utf-8") as f:
        f.writelines(lines)
    return True


def update_route_script(file_path, iface, ip, cidr, brd):
    with open(file_path, "r", encoding="utf-8") as f:
        content = f.read()

    new_content = content

    # 0) Fix interface name in comment / echo / commands
    # comment: # check ip address for device ens100np0
    new_content = re.sub(
        r'(^[ \t]*#.*?\bdevice[ \t]+)(ens[0-9A-Za-z]+)(\b.*$)',
        rf'\g<1>{iface}\g<3>',
        new_content,
        flags=re.M,
    )

    # echo text: echo "device ens100np0 no ip address..."
    new_content = re.sub(
        r'(\bdevice[ \t]+)(ens[0-9A-Za-z]+)(\b)',
        rf'\g<1>{iface}\g<3>',
        new_content,
    )

    # ip addr show ens100np0
    new_content = re.sub(
        r'(\bip[ \t]+addr[ \t]+show[ \t]+)(ens[0-9A-Za-z]+)(\b)',
        rf'\g<1>{iface}\g<3>',
        new_content,
    )

    # ip route add default via ... dev ens100np0 ...
    # also affects other route lines with dev oldiface
    new_content = re.sub(
        r'(\bdev[ \t]+)(ens[0-9A-Za-z]+)(\b)',
        rf'\g<1>{iface}\g<3>',
        new_content,
    )

    # ip rule show | grep rt_ens100np0
    new_content = re.sub(
        r'(\brt_)(ens[0-9A-Za-z]+)(\b)',
        rf'\g<1>{iface}\g<3>',
        new_content,
    )

    # ip rule add from all oif ens100np0 ...
    new_content = re.sub(
        r'(\boif[ \t]+)(ens[0-9A-Za-z]+)(\b)',
        rf'\g<1>{iface}\g<3>',
        new_content,
    )

    # 1) Update:
    # ip addr add 11.1.1.17/24 brd 11.1.1.255 dev ens11np0
    #
    # or:
    # ip addr add 11.1.1.17/24 dev ens11np0
    if brd:
        pattern_addr = re.compile(
            rf'(^[ \t]*ip[ \t]+addr[ \t]+add[ \t]+)\S+([ \t]+brd[ \t]+)\S+([ \t]+dev[ \t]+{re.escape(iface)}\b[^\n]*$)',
            flags=re.M
        )

        def repl_addr(m):
            return f"{m.group(1)}{cidr}{m.group(2)}{brd}{m.group(3)}"

        new_content = pattern_addr.sub(repl_addr, new_content)
    else:
        pattern_addr = re.compile(
            rf'(^[ \t]*ip[ \t]+addr[ \t]+add[ \t]+)\S+([ \t]+dev[ \t]+{re.escape(iface)}\b[^\n]*$)',
            flags=re.M
        )

        def repl_addr(m):
            return f"{m.group(1)}{cidr}{m.group(2)}"

        new_content = pattern_addr.sub(repl_addr, new_content)

    # 2) Update src IP in route line:
    # ip route add ... src 11.1.1.17 proto static
    pattern_route_src = re.compile(
        r'(^[ \t]*ip[ \t]+route[ \t]+add[^\n]*\bsrc[ \t]+)(\d+\.\d+\.\d+\.\d+)(\b[^\n]*$)',
        flags=re.M
    )

    def repl_route_src(m):
        return f"{m.group(1)}{ip}{m.group(3)}"

    new_content = pattern_route_src.sub(repl_route_src, new_content)

    # 3) Update IP rule source IP:
    # ip rule add from 11.1.1.17 table 101 priority 32761
    # Do NOT touch: ip rule add from all ...
    pattern_rule_from = re.compile(
        r'(^[ \t]*ip[ \t]+rule[ \t]+add[ \t]+from[ \t]+)(\d+\.\d+\.\d+\.\d+)([ \t]+table\b[^\n]*$)',
        flags=re.M
    )

    def repl_rule_from(m):
        return f"{m.group(1)}{ip}{m.group(3)}"

    new_content = pattern_rule_from.sub(repl_rule_from, new_content)

    if new_content == content:
        return False

    backup_file(file_path)
    with open(file_path, "w", encoding="utf-8") as f:
        f.write(new_content)
    return True


def main():
    overall_changed = False

    for iface in IFACES:
        try:
            info = get_iface_ipv4_info(iface)
        except Exception as e:
            print(f"[WARN] {iface}: {e}")
            continue

        ip = info["ip"]
        cidr = info["cidr"]
        brd = info["brd"]

        print(f"[INFO] {iface}: ip={ip}, cidr={cidr}, brd={brd}")

        netplan_file = os.path.join(NETPLAN_DIR, f"{iface}.yaml")
        route_file = os.path.join(ROUTABLE_DIR, f"config_rt_{iface}.sh")

        if os.path.isfile(netplan_file):
            try:
                changed = update_netplan_yaml(netplan_file, cidr)
                print(f"  {'[OK]' if changed else '[SKIP]'} netplan: {netplan_file}")
                overall_changed = overall_changed or changed
            except Exception as e:
                print(f"  [ERR] Failed to update netplan file {netplan_file}: {e}")
        else:
            print(f"  [WARN] Netplan file not found: {netplan_file}")

        if os.path.isfile(route_file):
            try:
                changed = update_route_script(route_file, iface, ip, cidr, brd)
                print(f"  {'[OK]' if changed else '[SKIP]'} route script: {route_file}")
                overall_changed = overall_changed or changed
            except Exception as e:
                print(f"  [ERR] Failed to update route script {route_file}: {e}")
        else:
            print(f"  [WARN] Route script file not found: {route_file}")

    print("[DONE] Processing completed")
    sys.exit(0)


if __name__ == "__main__":
    main()
