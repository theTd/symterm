#!/bin/bash
# Cleanup stale symtermd FUSE mounts and connections before daemon start.
# This prevents D-state processes from blocking service restart.

set -e

# Find all fuse.symterm mounts and extract connection IDs from mountinfo.
# In /proc/self/mountinfo, field 3 is major:minor; for FUSE the minor
# number matches the connection ID in /sys/fs/fuse/connections/.
abort_stale_connections() {
    local conn_ids=""
    while IFS= read -r line; do
        dev_field=$(echo "$line" | awk '{print $3}')
        minor=$(echo "$dev_field" | cut -d: -f2)
        if [ -n "$minor" ] && [ -d "/sys/fs/fuse/connections/$minor" ]; then
            conn_ids="$conn_ids $minor"
        fi
    done < <(grep 'fuse.symterm' /proc/self/mountinfo 2>/dev/null || true)

    for conn_id in $conn_ids; do
        if [ -w "/sys/fs/fuse/connections/$conn_id/abort" ]; then
            printf '1' > "/sys/fs/fuse/connections/$conn_id/abort" 2>/dev/null || true
        fi
    done
}

# Lazy-unmount any stale fuse.symterm mounts so the mount point is free.
detach_stale_mounts() {
    while IFS= read -r line; do
        mountpoint=$(echo "$line" | awk '{print $5}')
        mountpoint=$(printf '%b' "$mountpoint")  # unescape \040 etc.
        if [ -n "$mountpoint" ]; then
            fusermount3 -u -z "$mountpoint" 2>/dev/null || true
            # Fallback to MNT_DETACH
            umount -l "$mountpoint" 2>/dev/null || true
        fi
    done < <(grep 'fuse.symterm' /proc/self/mountinfo 2>/dev/null || true)
}

abort_stale_connections
detach_stale_mounts

# Give aborted threads a moment to exit D-state before the daemon starts.
sleep 1
