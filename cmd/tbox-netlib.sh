#!/bin/sh
# Sourced by tbox-run and tbox-network — not executed directly
# Provides: net_connect, net_rebuild_hosts, net_disconnect

TBOX_HOME="${TBOX_HOME:-$HOME/.tbox}"
NETWORKS_DIR="$TBOX_HOME/networks"
CONTAINERS_DIR="$TBOX_HOME/containers"

# Allocate a stable internal port for a container on a network
# Uses a hash of container name to get a port in range 40000-49999
net_alloc_port() {
    CNAME="$1"
    # Simple hash: sum ascii values mod 10000 + 40000
    PORT=$(printf '%s' "$CNAME" | od -A n -t u1 | tr ' ' '\n' | \
        grep -v '^$' | awk '{s+=$1} END {print (s % 10000) + 40000}')
    echo "$PORT"
}

# Rebuild /etc/hosts for every container in a network
net_rebuild_hosts() {
    NETNAME="$1"
    NDIR="$NETWORKS_DIR/$NETNAME"
    [ -f "$NDIR/members" ] || return

    # Kill old proxies for this network
    while read pid; do
        kill "$pid" 2>/dev/null || true
    done < "$NDIR/proxies" 2>/dev/null
    : > "$NDIR/proxies"

    # Collect all member container ports
    MEMBERS=$(cat "$NDIR/members")

    for member in $MEMBERS; do
        CDIR="$CONTAINERS_DIR/$member"
        [ -d "$CDIR" ] || continue

        # Get this container's exposed port
        EXPOSED_PORT=$(cat "$CDIR/network_port" 2>/dev/null || echo "")
        [ -z "$EXPOSED_PORT" ] && continue

        # Rebuild /etc/hosts inside every OTHER container on this network
        for target in $MEMBERS; do
            [ "$target" = "$member" ] && continue
            TDIR="$CONTAINERS_DIR/$target"
            [ -d "$TDIR/rootfs" ] || continue

            # Allocate a proxy port for this member as seen from target
            PROXY_PORT=$(net_alloc_port "${NETNAME}_${member}_${target}")

            # Start socat proxy: proxy_port -> member's actual port
            socat TCP4-LISTEN:$PROXY_PORT,fork,reuseaddr \
                  TCP4:127.0.0.1:$EXPOSED_PORT >> "$NDIR/proxy.log" 2>&1 &
            echo $! >> "$NDIR/proxies"

            # Inject /etc/hosts entry inside target container
            HOSTS="$TDIR/rootfs/etc/hosts"
            # Remove old entry for this member
            grep -v " $member$" "$HOSTS" > "$HOSTS.tmp" 2>/dev/null && \
                mv "$HOSTS.tmp" "$HOSTS" || true
            # Add new entry
            echo "127.0.0.1  $member" >> "$HOSTS"

            echo "    [$NETNAME] $target -> $member via :$PROXY_PORT -> :$EXPOSED_PORT"
        done
    done
}

# Connect a container to a network and set up routing
net_connect() {
    NETNAME="$1"
    CNAME="$2"
    EXPOSED_PORT="$3"   # the port this container listens on internally

    NDIR="$NETWORKS_DIR/$NETNAME"
    mkdir -p "$NDIR"
    [ -f "$NDIR/name" ] || echo "$NETNAME" > "$NDIR/name"
    [ -f "$NDIR/created" ] || date +%s > "$NDIR/created"
    touch "$NDIR/members" "$NDIR/proxies"

    CDIR="$CONTAINERS_DIR/$CNAME"

    # Save this container's network port
    echo "$EXPOSED_PORT" > "$CDIR/network_port"
    echo "$NETNAME"      > "$CDIR/network"

    # Add to members
    grep -qx "$CNAME" "$NDIR/members" 2>/dev/null || \
        echo "$CNAME" >> "$NDIR/members"

    # Ensure /etc/hosts exists in this container
    mkdir -p "$CDIR/rootfs/etc"
    touch "$CDIR/rootfs/etc/hosts"
    grep -q "127.0.0.1.*localhost" "$CDIR/rootfs/etc/hosts" 2>/dev/null || \
        printf '127.0.0.1\tlocalhost\n::1\tlocalhost\n' \
            >> "$CDIR/rootfs/etc/hosts"

    # Add self entry
    grep -v " $CNAME$" "$CDIR/rootfs/etc/hosts" > \
        "$CDIR/rootfs/etc/hosts.tmp" 2>/dev/null && \
        mv "$CDIR/rootfs/etc/hosts.tmp" "$CDIR/rootfs/etc/hosts" || true
    echo "127.0.0.1  $CNAME" >> "$CDIR/rootfs/etc/hosts"

    # Rebuild routing for all members
    net_rebuild_hosts "$NETNAME"
}
