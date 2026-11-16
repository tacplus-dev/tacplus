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
host 'sysctl -w kernel.apparmor_restrict_unprivileged_userns=0'
FIX='command -v setcap getcap; dpkg -S "$(command -v setcap)" 2>&1; command -v setcap >/dev/null || { apt-get update -qq && apt-get install -y -qq --no-install-recommends libcap2-bin >/dev/null; }
chmod u-s /usr/bin/newuidmap /usr/bin/newgidmap && setcap cap_setuid=ep /usr/bin/newuidmap && setcap cap_setgid=ep /usr/bin/newgidmap && getcap /usr/bin/newuidmap /usr/bin/newgidmap'
echo "##### fix: file caps, default docker caps (the workflow's BUILDKIT_CONTAINER)"
mkdir -p /tmp/ctx && cat > /tmp/ctx/Dockerfile <<'DF'
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends libcap2-bin uidmap && rm -rf /var/lib/apt/lists/* \
    && chmod u-s /usr/bin/newuidmap /usr/bin/newgidmap \
    && setcap cap_setuid=ep /usr/bin/newuidmap && setcap cap_setgid=ep /usr/bin/newgidmap
DF
docker run --rm "${OPTS[@]}" -i "$IMG" bash -c "$FIX"'
mkdir -p /ctx && cat > /ctx/Dockerfile

setpriv --reuid=build --regid=build --init-groups unshare --user --map-auto --map-root-user cat /proc/self/uid_map; echo "map-auto rc=$?"
timeout 90 tacplus-image-build start; echo "start rc=$?"; tail -5 /run/buildkit/buildkitd.log
echo "##### the exported image keeps the file capability (rewrite-timestamp)"
SOURCE_DATE_EPOCH=1700000000 tacplus-image-build -f /ctx/Dockerfile --output type=oci,dest=/tmp/x.tar /ctx 2>&1 | tail -3
python3 -I - <<PY
import tarfile, json, gzip, io
t = tarfile.open("/tmp/x.tar")
idx = json.load(t.extractfile("index.json"))
def blob(d): return t.extractfile("blobs/sha256/" + d.split(":")[1]).read()
m = json.loads(blob(idx["manifests"][0]["digest"]))
if "manifests" in m: m = json.loads(blob(m["manifests"][0]["digest"]))
for l in m["layers"]:
    data = blob(l["digest"])
    lt = tarfile.open(fileobj=io.BytesIO(data))
    for e in lt:
        if e.name.endswith(("newuidmap", "newgidmap")):
            print(e.name, oct(e.mode), sorted(e.pax_headers))
PY' < /tmp/ctx/Dockerfile 2>&1
host 'dmesg | grep -i apparmor | tail -5'
echo "##### done"
exit 1
