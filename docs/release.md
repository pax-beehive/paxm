# paxm Release Checklist

`paxm` binary releases are built from git tags.

## Automated Release

1. Make sure `main` is up to date and clean.
2. Run local validation:

   ```bash
   go test ./...
   go vet ./...
   VERSION=v0.1.0 scripts/build-release.sh
   ```

3. Commit and push code changes.
4. Create and push a version tag:

   ```bash
   git tag v0.1.0
   git push origin main
   git push origin v0.1.0
   ```

5. GitHub Actions runs `.github/workflows/release.yml` and publishes release
   assets.

## Local Release Build

The local build script writes artifacts to `dist/`:

```text
paxm_v0.1.0_darwin_amd64.tar.gz
paxm_v0.1.0_darwin_arm64.tar.gz
paxm_v0.1.0_linux_amd64.tar.gz
paxm_v0.1.0_linux_arm64.tar.gz
paxm_v0.1.0_windows_amd64.zip
paxm_v0.1.0_windows_arm64.zip
SHA256SUMS
```

Each archive contains the `paxm` binary and the project README.

## Versioning

Use semver-style tags with a leading `v`, for example `v0.1.0`.

The release script injects the tag into `paxm version` through Go linker flags.
Unreleased local builds report `dev` unless `VERSION` is set.

## Verification

After downloading an asset:

```bash
shasum -a 256 -c SHA256SUMS
tar -xzf paxm_v0.1.0_darwin_arm64.tar.gz
./paxm_v0.1.0_darwin_arm64/paxm version
```

For Windows assets, use `unzip` instead of `tar`.
