#!/bin/bash
set -u -o pipefail

INVENTORY="/home/wangxuanqi/workspace/100g-ips.list"
PING_COUNT=3
PING_SIZE=4172
RDMA_TIMEOUT=20
RDMA_GID_PREFER="${RDMA_GID_PREFER:-v2}"

nics=(ens11np0 ens13np0 ens15np0 ens17np0)
ips_25g=(10.101.9.11 10.101.9.12 10.101.9.13 10.101.9.14 10.101.9.15 10.101.9.16 10.101.9.17 10.101.9.18)
rdma_devs=(mlx5_2 mlx5_3 mlx5_4 mlx5_5)

declare -A NIC_TO_GATEWAY=(
    [ens11np0]=10.101.9.1
    [ens13np0]=10.101.9.2
    [ens15np0]=10.101.9.3
    [ens17np0]=10.101.9.4
)

SSH_OPTS=(
    -o BatchMode=yes
    -o ConnectTimeout=5
    -o ServerAliveInterval=5
    -o ServerAliveCountMax=2
)

log_info() {
    echo "[INFO] $*"
}

log_warn() {
    echo "[WARN] $*" >&2
}

log_error() {
    echo "[ERROR] $*" >&2
}

require_cmd() {
    local cmd="$1"
    command -v "$cmd" >/dev/null 2>&1 || {
        log_error "Required command not found: $cmd"
        return 1
    }
}

run_ssh() {
    local host="$1"
    local cmd="$2"
    ssh "${SSH_OPTS[@]}" "$host" "$cmd"
}

inventory_hosts() {
    awk '
        /^[[:space:]]*#/ {next}
        /^[[:space:]]*$/ {next}
        /^[[:space:]]*\[/ {next}
        {print $1}
    ' "$INVENTORY"
}

detect_local_server_ip() {
    if [[ -n "${LOCAL_RDMA_SERVER_IP:-}" ]]; then
        printf '%s\n' "$LOCAL_RDMA_SERVER_IP"
        return 0
    fi

    local -a local_ips hosts
    local ip host

    mapfile -t local_ips < <(hostname -I 2>/dev/null | tr ' ' '\n' | sed '/^$/d')
    mapfile -t hosts < <(inventory_hosts)

    for ip in "${local_ips[@]}"; do
        for host in "${hosts[@]}"; do
            if [[ "$ip" == "$host" ]]; then
                printf '%s\n' "$ip"
                return 0
            fi
        done
    done

    if ((${#local_ips[@]} > 0)); then
        printf '%s\n' "${local_ips[0]}"
        return 0
    fi

    return 1
}

get_iface_ip() {
    local host="$1"
    local iface="$2"
    run_ssh "$host" "ip -4 -o addr show ${iface} 2>/dev/null | awk '{print \$4}' | cut -d/ -f1 | head -n1" 2>/dev/null || true
}

run_ping_test() {
    local host="$1"
    local iface="$2"
    local src_ip="$3"
    local target_ip="$4"

    log_info "[$host][$iface] ping target $target_ip by iface"
    if run_ssh "$host" "ping -I ${iface} -c ${PING_COUNT} -s ${PING_SIZE} ${target_ip} -M do"; then
        log_info "[$host][$iface] iface ping to $target_ip OK"
    else
        log_warn "[$host][$iface] iface ping to $target_ip FAILED"
    fi

    log_info "[$host][$iface] ping target $target_ip by source IP $src_ip"
    if run_ssh "$host" "ping -I ${src_ip} -c ${PING_COUNT} -s ${PING_SIZE} ${target_ip} -M do"; then
        log_info "[$host][$iface] src ping to $target_ip OK"
    else
        log_warn "[$host][$iface] src ping to $target_ip FAILED"
    fi
}

get_same_subnet_prefix() {
    local iface="$1"
    case "$iface" in
        ens11np0) echo "11.1.1" ;;
        ens13np0) echo "11.1.2" ;;
        ens15np0) echo "11.1.3" ;;
        ens17np0) echo "11.1.4" ;;
        *) return 1 ;;
    esac
}

gateway_ping() {
    echo "===== Gateway ping test ====="
    local host iface gw self_ip

    for host in "${ips_25g[@]}"; do
        echo "=== Testing host: $host ==="
        for iface in "${nics[@]}"; do
            gw="${NIC_TO_GATEWAY[$iface]:-}"
            if [[ -z "$gw" ]]; then
                log_warn "Unknown iface: $iface"
                continue
            fi

            self_ip="$(get_iface_ip "$host" "$iface")"
            if [[ -z "$self_ip" ]]; then
                log_warn "[$host][$iface] no IPv4 address, skip"
                continue
            fi

            if run_ssh "$host" "ping -I ${iface} -c ${PING_COUNT} -s ${PING_SIZE} ${gw} -M do"; then
                log_info "[$host][$iface] iface ping to gateway OK"
            else
                log_warn "[$host][$iface] iface ping to gateway FAILED"
            fi

            if [[ "$self_ip" == "$gw" ]]; then
                log_info "[$host][$iface] source IP equals gateway, skip source-IP ping"
                continue
            fi

            if run_ssh "$host" "ping -I ${self_ip} -c ${PING_COUNT} -s ${PING_SIZE} ${gw} -M do"; then
                log_info "[$host][$iface] src ping to gateway OK"
            else
                log_warn "[$host][$iface] src ping to gateway FAILED"
            fi
        done
    done
}

same_nic_ping() {
    echo "===== Same-NIC subnet ping test ====="
    local host iface prefix self_ip target_ip n

    for host in "${ips_25g[@]}"; do
        echo "=== Testing host: $host ==="
        for iface in "${nics[@]}"; do
            prefix="$(get_same_subnet_prefix "$iface" || true)"
            if [[ -z "$prefix" ]]; then
                log_warn "Unknown iface: $iface"
                continue
            fi

            self_ip="$(get_iface_ip "$host" "$iface")"
            if [[ -z "$self_ip" ]]; then
                log_warn "[$host][$iface] no IPv4 address, skip"
                continue
            fi

            for n in {14..22}; do
                target_ip="${prefix}.${n}"
                if [[ "$self_ip" == "$target_ip" ]]; then
                    log_info "[$host][$iface] source IP equals target IP ($target_ip), skip current target"
                    continue
                fi
                run_ping_test "$host" "$iface" "$self_ip" "$target_ip"
            done
        done
    done
}

cross_nic_ping() {
    echo "===== Cross-NIC subnet ping test ====="
    local host iface other_iface self_ip prefix target_ip n

    for host in "${ips_25g[@]}"; do
        echo "=== Testing host: $host ==="
        for iface in "${nics[@]}"; do
            self_ip="$(get_iface_ip "$host" "$iface")"
            if [[ -z "$self_ip" ]]; then
                log_warn "[$host][$iface] no IPv4 address, skip"
                continue
            fi

            for other_iface in "${nics[@]}"; do
                [[ "$other_iface" == "$iface" ]] && continue
                prefix="$(get_same_subnet_prefix "$other_iface" || true)"
                [[ -z "$prefix" ]] && continue

                for n in {14..22}; do
                    target_ip="${prefix}.${n}"
                    if [[ "$self_ip" == "$target_ip" ]]; then
                        log_info "[$host][$iface] source IP equals target IP ($target_ip), skip current target"
                        continue
                    fi
                    run_ping_test "$host" "$iface" "$self_ip" "$target_ip"
                done
            done
        done
    done
}

run_ansible_cmd() {
    local title="$1"
    local cmd="$2"
    local use_bash="${3:-0}"

    echo "===== $title ====="
    if [[ "$use_bash" == "1" ]]; then
        ansible -i "$INVENTORY" all -b -m shell -a "$cmd" -e "ansible_shell_executable=/bin/bash"
    else
        ansible -i "$INVENTORY" all -b -m shell -a "$cmd"
    fi
}

check_machine() {
    echo "===== Check machine environment ====="
    require_cmd ansible || return 1
    run_ansible_cmd "CPU info" 'lscpu'
    run_ansible_cmd "Memory info" 'free -g'
    run_ansible_cmd "XPU info" 'xpu-smi'
    run_ansible_cmd "Block device info" 'lsblk -a'
    run_ansible_cmd "Mellanox PCI devices" 'lspci | grep -i mellanox'
    run_ansible_cmd "OS release" 'cat /etc/os-release'
    run_ansible_cmd "Docker version" 'docker --version'
    run_ansible_cmd "XPU version" 'xpu-smi -a | grep -i version'
}

check_network() {
    echo "===== Check network environment ====="
    require_cmd ansible || return 1
    run_ansible_cmd "IP address" 'ip addr'
    run_ansible_cmd "Main routing table" 'ip route show'
    run_ansible_cmd "Policy rules" 'ip rule show'
    run_ansible_cmd "Netplan files" 'shopt -s nullglob; files=(/etc/netplan/*.yaml); if ((${#files[@]})); then cat "${files[@]}"; else echo "No netplan yaml files found"; fi' 1
    run_ansible_cmd "systemd-networkd files" 'shopt -s nullglob; files=(/etc/systemd/network/*); if ((${#files[@]})); then cat "${files[@]}"; else echo "No files under /etc/systemd/network"; fi' 1
    run_ansible_cmd "Policy routing tables 101-104" 'for i in {101..104}; do echo "===== table $i ====="; ip route show table "$i"; ip rule show | grep "lookup $i" || true; done' 1
    run_ansible_cmd "LLDP info" 'for i in ens11np0 ens13np0 ens15np0 ens17np0; do echo "===== Interface $i ====="; lldptool -t -n -i "$i" | grep -E -A1 "Port ID TLV|Port Description TLV|System Name TLV" || true; done' 1
    run_ansible_cmd "mlnx_qos info" 'for i in ens11np0 ens13np0 ens15np0 ens17np0; do echo "===== Interface $i ====="; mlnx_qos -i "$i"; done' 1
    run_ansible_cmd "ethtool ring buffer" 'for i in ens11np0 ens13np0 ens15np0 ens17np0; do echo "===== Interface $i ====="; ethtool -g "$i"; done' 1
}

check_system() {
    echo "===== Check system environment ====="
    require_cmd ansible || return 1
    run_ansible_cmd "Kernel cmdline iommu" 'cat /proc/cmdline | grep iommu || true' 1
    run_ansible_cmd "sysctl tail" 'tail -n 25 /etc/sysctl.conf' 1
}

cleanup_bg_pid() {
    local pid="${1:-}"
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
        kill "$pid" >/dev/null 2>&1 || true
        wait "$pid" >/dev/null 2>&1 || true
    fi
}

parse_show_gids_stream() {
    local dev="$1"
    local ver="$2"
    awk -v dev="$dev" -v ver="$ver" '
        $1 == dev && $5 ~ /^[0-9]+\./ && $6 == ver { print $3 " " $5; exit }
    '
}

get_gid_info_local_show_gids() {
    local dev="$1"
    local ver="$2"
    show_gids 2>/dev/null | parse_show_gids_stream "$dev" "$ver"
}

get_gid_info_remote_show_gids() {
    local host="$1"
    local dev="$2"
    local ver="$3"
    run_ssh "$host" "show_gids 2>/dev/null | awk -v dev='$dev' -v ver='$ver' '\$1 == dev && \$5 ~ /^[0-9]+\\./ && \$6 == ver { print \$3 \" \" \$5; exit }'" 2>/dev/null || true
}

convert_gid_to_ipv4() {
    local gid="$1"
    local p7 p8

    IFS=':' read -r _ _ _ _ _ _ p7 p8 <<< "$gid"
    [[ -n "$p7" && -n "$p8" ]] || return 1

    printf '%d.%d.%d.%d\n' \
        "$((16#${p7:0:2}))" \
        "$((16#${p7:2:2}))" \
        "$((16#${p8:0:2}))" \
        "$((16#${p8:2:2}))"
}

get_gid_info_local_sysfs() {
    local dev="$1"
    local ver="$2"
    local base="/sys/class/infiniband/${dev}/ports/1"
    local idx_path idx type gid ipv4

    [[ -d "$base" ]] || return 1

    for idx_path in "$base"/gids/*; do
        [[ -e "$idx_path" ]] || continue
        idx="${idx_path##*/}"
        type="$(cat "$base/gid_attrs/types/$idx" 2>/dev/null || true)"
        gid="$(cat "$base/gids/$idx" 2>/dev/null || true)"

        [[ "$type" == *"$ver"* ]] || continue
        [[ "$gid" == 0000:0000:0000:0000:0000:ffff:* ]] || continue

        ipv4="$(convert_gid_to_ipv4 "$gid" 2>/dev/null || true)"
        if [[ -n "$ipv4" ]]; then
            printf '%s %s\n' "$idx" "$ipv4"
            return 0
        fi
    done

    return 1
}

get_gid_info_remote_sysfs() {
    local host="$1"
    local dev="$2"
    local ver="$3"

    run_ssh "$host" "bash -lc '
base=/sys/class/infiniband/${dev}/ports/1
[[ -d \"\$base\" ]] || exit 1
for idx_path in \"\$base\"/gids/*; do
  [[ -e \"\$idx_path\" ]] || continue
  idx=\"\${idx_path##*/}\"
  type=\"\$(cat \"\$base/gid_attrs/types/\$idx\" 2>/dev/null || true)\"
  gid=\"\$(cat \"\$base/gids/\$idx\" 2>/dev/null || true)\"
  [[ \"\$type\" == *${ver}* ]] || continue
  [[ \"\$gid\" == 0000:0000:0000:0000:0000:ffff:* ]] || continue
  IFS=: read -r _ _ _ _ _ _ p7 p8 <<< \"\$gid\"
  printf \"%s %d.%d.%d.%d\n\" \
    \"\$idx\" \
    \"\$((16#\${p7:0:2}))\" \
    \"\$((16#\${p7:2:2}))\" \
    \"\$((16#\${p8:0:2}))\" \
    \"\$((16#\${p8:2:2}))\"
  exit 0
done
exit 1
'" 2>/dev/null || true
}

get_gid_info_local() {
    local dev="$1"
    local ver="$2"

    if command -v show_gids >/dev/null 2>&1; then
        get_gid_info_local_show_gids "$dev" "$ver" && return 0
    fi

    get_gid_info_local_sysfs "$dev" "$ver"
}

get_gid_info_remote() {
    local host="$1"
    local dev="$2"
    local ver="$3"

    if run_ssh "$host" "command -v show_gids >/dev/null 2>&1"; then
        get_gid_info_remote_show_gids "$host" "$dev" "$ver" && return 0
    fi

    get_gid_info_remote_sysfs "$host" "$dev" "$ver"
}

choose_common_gid_ver() {
    local server_dev="$1"
    local client_host="$2"
    local client_dev="$3"

    local -a vers
    local local_info remote_info ver

    if [[ "$RDMA_GID_PREFER" == "v1" ]]; then
        vers=(v1 v2)
    else
        vers=(v2 v1)
    fi

    for ver in "${vers[@]}"; do
        local_info="$(get_gid_info_local "$server_dev" "$ver" || true)"
        remote_info="$(get_gid_info_remote "$client_host" "$client_dev" "$ver" || true)"
        if [[ -n "$local_info" && -n "$remote_info" ]]; then
            printf '%s\n' "$ver"
            return 0
        fi
    done

    return 1
}

run_rdma_pair_test() {
    local test_bin="$1"
    local server_dev="$2"
    local client_host="$3"
    local client_dev="$4"
    local control_ip="$5"

    local gid_ver local_info remote_info
    local server_gid_index server_dev_ip client_gid_index client_dev_ip
    local server_cmd client_cmd server_pid="" server_log

    gid_ver="$(choose_common_gid_ver "$server_dev" "$client_host" "$client_dev" || true)"
    if [[ -z "$gid_ver" ]]; then
        log_warn "No common IPv4 GID found for local ${server_dev} <-> remote ${client_host}:${client_dev}"
        return 1
    fi

    local_info="$(get_gid_info_local "$server_dev" "$gid_ver" || true)"
    remote_info="$(get_gid_info_remote "$client_host" "$client_dev" "$gid_ver" || true)"

    server_gid_index="${local_info%% *}"
    server_dev_ip="${local_info#* }"
    client_gid_index="${remote_info%% *}"
    client_dev_ip="${remote_info#* }"

    if [[ -z "$server_gid_index" || -z "$client_gid_index" || -z "$server_dev_ip" || -z "$client_dev_ip" ]]; then
        log_warn "Failed to resolve GID index/IP for local ${server_dev} <-> remote ${client_host}:${client_dev}"
        return 1
    fi

    if [[ -z "$control_ip" ]]; then
        control_ip="$server_dev_ip"
    fi

    server_log="/tmp/${test_bin}_${server_dev}_$$.log"

    case "$test_bin" in
        ib_write_bw)
            server_cmd="timeout ${RDMA_TIMEOUT}s ib_write_bw -d ${server_dev} -x ${server_gid_index} -q 1 > ${server_log} 2>&1"
            client_cmd="timeout ${RDMA_TIMEOUT}s ib_write_bw -d ${client_dev} -x ${client_gid_index} -q 1 --report_gbits ${control_ip}"
            ;;
        ib_read_bw)
            server_cmd="timeout ${RDMA_TIMEOUT}s ib_read_bw -a -d ${server_dev} -x ${server_gid_index} > ${server_log} 2>&1"
            client_cmd="timeout ${RDMA_TIMEOUT}s ib_read_bw -a -d ${client_dev} -x ${client_gid_index} --report_gbits ${control_ip}"
            ;;
        *)
            log_error "Unsupported RDMA test binary: $test_bin"
            return 1
            ;;
    esac

    bash -c "$server_cmd" &
    server_pid=$!
    sleep 2

    if ! kill -0 "$server_pid" 2>/dev/null; then
        log_warn "Local RDMA server exited early: tool=${test_bin}, dev=${server_dev}, x=${server_gid_index}"
        return 1
    fi

    log_info "RDMA test: tool=${test_bin}, gid_ver=${gid_ver}, local ${server_dev}(x=${server_gid_index},ip=${server_dev_ip}) <-> remote ${client_host}:${client_dev}(x=${client_gid_index},ip=${client_dev_ip}), control_ip=${control_ip}"
    if run_ssh "$client_host" "$client_cmd"; then
        log_info "RDMA test OK: local ${server_dev} <-> remote ${client_host}:${client_dev}"
        cleanup_bg_pid "$server_pid"
        return 0
    else
        log_warn "RDMA test FAILED: local ${server_dev} <-> remote ${client_host}:${client_dev}"
        cleanup_bg_pid "$server_pid"
        return 1
    fi
}

check_rdma() {
    echo "===== Check RDMA connectivity ====="
    require_cmd ssh || return 1
    require_cmd timeout || return 1
    require_cmd ib_write_bw || return 1
    require_cmd ib_read_bw || return 1

    local control_ip=""
    local host local_dev peer_dev
    local -a hosts

    [[ -f "$INVENTORY" ]] || {
        log_error "Inventory file not found: $INVENTORY"
        return 1
    }

    mapfile -t hosts < <(inventory_hosts)
    if ((${#hosts[@]} == 0)); then
        log_error "No valid hosts found in inventory: $INVENTORY"
        return 1
    fi

    control_ip="$(detect_local_server_ip || true)"
    if [[ -z "$control_ip" ]]; then
        log_error "Failed to detect local server IP. You can export LOCAL_RDMA_SERVER_IP=<your_inventory_ip> and retry."
        return 1
    fi

    log_info "Using control IP: $control_ip"
    log_info "Preferred GID version: $RDMA_GID_PREFER"

    echo "===== Cross-device RDMA write_bw test ====="
    for local_dev in "${rdma_devs[@]}"; do
        echo "=== Local device: $local_dev ==="
        for host in "${hosts[@]}"; do
            [[ "$host" == "$control_ip" ]] && continue
            for peer_dev in "${rdma_devs[@]}"; do
                [[ "$peer_dev" == "$local_dev" ]] && continue
                run_rdma_pair_test "ib_write_bw" "$local_dev" "$host" "$peer_dev" "$control_ip"
            done
        done
    done

    echo "===== Same-device RDMA read_bw test ====="
    for local_dev in "${rdma_devs[@]}"; do
        echo "=== Local device: $local_dev ==="
        for host in "${hosts[@]}"; do
            [[ "$host" == "$control_ip" ]] && continue
            run_rdma_pair_test "ib_read_bw" "$local_dev" "$host" "$local_dev" "$control_ip"
        done
    done
}

check_xccl() {
    echo "===== Single-node XCCL test ====="
    require_cmd ansible || return 1
    run_ansible_cmd "Restart xccl-test-aio container" 'docker restart xccl-test-aio'
    sleep 3
    run_ansible_cmd "Start sshd in container" 'docker exec xccl-test-aio /etc/init.d/ssh start || true'
    run_ansible_cmd "Run single-node xccl perf" 'docker exec -e BKCL_SOCKET_IFNAME=bond0 -e BKCL_RDMA_NICS=ens11np0,ens11np0,ens13np0,ens13np0,ens15np0,ens15np0,ens17np0,ens17np0 xccl-test-aio mpirun -np 8 /test_aio/xccl-test-output/systest/xccl_perf -O allReduce -x 1 -b 1024 -e 256M -f 2'

    echo "===== Multi-node XCCL test ====="
    docker exec xccl-test-aio sh /test_aio/test_flat_ring.sh 72 /test_aio/hosts allReduce fp16 0 1M 2G 20
    docker exec xccl-test-aio sh /test_aio/test_mutidp.sh 72 /test_aio/hosts allReduce fp16 0 1M 2G 20 8 0
}

usage() {
    cat <<'EOF'
Usage:
  ./net_check.sh [all|gateway|same|cross|machine|network|system|rdma|xccl]

Defaults:
  no argument => same + cross + rdma

Environment variables:
  LOCAL_RDMA_SERVER_IP=<inventory_ip>   Override detected local control IP
  RDMA_GID_PREFER=v2|v1                 Prefer RoCE GID version, default v2
EOF
}

main() {
    local action="${1:-default}"

    case "$action" in
        all)
            check_machine
            check_network
            check_system
            gateway_ping
            same_nic_ping
            cross_nic_ping
            check_rdma
            check_xccl
            ;;
        gateway)
            gateway_ping
            ;;
        same)
            same_nic_ping
            ;;
        cross)
            cross_nic_ping
            ;;
        machine)
            check_machine
            ;;
        network)
            check_network
            ;;
        system)
            check_system
            ;;
        rdma)
            check_rdma
            ;;
        xccl)
            check_xccl
            ;;
        default)
            same_nic_ping
            cross_nic_ping
            check_rdma
            ;;
        -h|--help|help)
            usage
            ;;
        *)
            log_error "Unknown action: $action"
            usage
            return 1
            ;;
    esac
}

main "$@"
