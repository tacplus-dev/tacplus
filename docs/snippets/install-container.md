```bash
# Once, and whenever you move to a newer release: pin it, and commit the pin.
docker run --rm -v "$PWD:/workspace" -w /workspace \
  ghcr.io/tacplus-dev/tacplus/build:2.6 tacplus pin
git add tacplus.lock && git commit -m "Pin tacplus"

# Every build: the pinned image, by digest, running the pinned tacplus.
image="$(sed -n 's/^build_image: //p' tacplus.lock)"
docker run --rm -v "$PWD:/workspace" -w /workspace "$image" \
  tacplus build --config tacplus.yaml --src proto
```
