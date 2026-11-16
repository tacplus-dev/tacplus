#!/usr/bin/env bash
# TEMPORARY (#830): what keeps rootless BuildKit's newuidmap from writing a
# uid_map on GitHub's runner. Reverted before the fix lands.
set -uo pipefail
IMG=ghcr.io/tacplus-dev/tacplus/build@sha256:67259a041fd0c8e71c1c43e5d5e86b64e5d287aaf3d17652a06e5c3b8e921c4b
OPTS=(--security-opt seccomp=unconfined --security-opt apparmor=unconfined)
host() { docker run --rm --privileged --pid=host "$IMG" nsenter -t 1 -m -u -i -n -p -- sh -c "$1"; }
probe() {
  echo "##### probe: $1"
  shift
  docker run --rm "${OPTS[@]}" "$@" "$IMG" bash -c '
    grep -E "NoNewPrivs|CapEff|CapBnd|Seccomp" /proc/self/status; echo "label: $(cat /proc/self/attr/current 2>&1)"; echo "uid_map: $(cat /proc/self/uid_map)"
    ls -l /usr/bin/newuidmap; cat /etc/subuid
    setpriv --reuid=build --regid=build --init-groups bash -c "grep -E \"NoNewPrivs|CapEff\" /proc/self/status; unshare --user --map-auto --map-root-user sh -c \"echo map-auto ok; cat /proc/self/uid_map\" 2>&1"
    timeout 90 tacplus-image-build start; rc=$?; echo "start rc=$rc"; cat /run/buildkit/buildkitd.log | tail -20' 2>&1
}
echo "##### host"
docker version 2>&1 | head -30
host 'uname -a; cat /etc/os-release | head -3; for k in kernel.apparmor_restrict_unprivileged_userns kernel.apparmor_restrict_unprivileged_unconfined kernel.apparmor_restrict_unprivileged_userns_force kernel.apparmor_restrict_unprivileged_userns_complain kernel.unprivileged_userns_clone user.max_user_namespaces; do sysctl $k 2>&1; done; ls /proc/sys/kernel | grep -i -E "apparmor|userns"; echo "--- daemon.json"; cat /etc/docker/daemon.json 2>&1; echo "--- profiles"; wc -l /sys/kernel/security/apparmor/profiles; grep -i -E "userns|uidmap|rootless|buildkit|unconfined|docker|runc|containerd" /sys/kernel/security/apparmor/profiles; ls /etc/apparmor.d/ | tr "\n" " "; echo; cat /etc/apparmor.d/unprivileged_userns 2>&1 | grep -v "^#" | head -40; grep -l -r newuidmap /etc/apparmor.d 2>/dev/null; dpkg -l apparmor docker-ce containerd.io runc 2>&1 | tail -5; cat /proc/cmdline'
probe "as the workflow, sysctl as booted"
host 'dmesg | grep -i -E "apparmor|audit" | tail -20'
host 'sysctl -w kernel.apparmor_restrict_unprivileged_userns=0; sysctl kernel.apparmor_restrict_unprivileged_userns'
probe "sysctl restrict_unprivileged_userns=0"
host 'dmesg | grep -i -E "apparmor|audit" | tail -20'
probe "plus systempaths=unconfined" --security-opt systempaths=unconfined
probe "privileged (control)" --privileged
echo "##### done"
exit 1
