```bash
docker run --rm --pull always -v "$PWD:/workspace" \
  ghcr.io/tacplus-dev/tacplus/tacplus:2.3 \
  tacplus build --config tacplus.yaml --src proto
```
