#!/bin/sh
# Container entrypoint: node.sh <role> [default gateway]
set -eu
role=$1
gw=${2:-}

iface_of() { ip -o -4 addr show | awk -v ip="$1" '$4 ~ "^"ip"/" {print $2}'; }

[ -n "$gw" ] && ip route replace default via "$gw"

case $role in
isp)
  sysctl -qw net.ipv4.ip_forward=1
  ;;
echo)
  # Replies with the caller's address, as seen by this host.
  mkdir -p /www/cgi-bin
  printf '#!/bin/sh\nprintf "Content-Type: text/plain\\r\\n\\r\\n%%s\\n" "$REMOTE_ADDR"\n' >/www/cgi-bin/ip
  chmod +x /www/cgi-bin/ip
  busybox httpd -p 0.0.0.0:80 -h /www
  ;;
home1)
  # Same network as Home2 LAN, to force the alias path.
  ip link add lan-dup type dummy
  ip addr add 192.168.77.1/24 dev lan-dup
  ip link set lan-dup up
  # Rules of another program at our priorities. Three differ from ours only
  # by one extra attribute, one has our selector but no protocol: a delete
  # the kernel could match against them would remove them first.
  for fam in -4 -6; do
    ip $fam rule add priority 5250 fwmark 0x123 lookup main suppress_prefixlength 0
    ip $fam rule add priority 5251 fwmark 0x5256 iif lo lookup main
    ip $fam rule add priority 5252 fwmark 0x123 iif lo lookup 21078
    ip $fam rule add priority 5252 iif lo lookup 21078
  done
  # Start values the server must change and later restore.
  sysctl -qw net.ipv4.conf.all.src_valid_mark=0
  for f in /proc/sys/net/ipv4/conf/*/rp_filter; do echo 1 >"$f"; done
  mkdir -p /work
  ;;
nat2)
  sysctl -qw net.ipv4.ip_forward=1
  iptables -t nat -A POSTROUTING -s 192.168.77.0/24 -o "$(iface_of 10.201.2.10)" -j MASQUERADE
  ;;
home2)
  # Like a Docker host: forwarding on, FORWARD policy DROP, rules of others.
  sysctl -qw net.ipv4.ip_forward=1
  iptables -P FORWARD DROP
  iptables -A FORWARD -s 198.51.100.0/24 -m comment --comment rvt-e2e-sentinel -j ACCEPT
  iptables -t nat -A POSTROUTING -d 198.51.100.1/32 -m comment --comment rvt-e2e-sentinel -j RETURN
  mkdir -p /work
  ;;
esac

touch /tmp/ready
exec sleep infinity
