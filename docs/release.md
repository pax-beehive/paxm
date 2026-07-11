# paxm Release Checklist

`paxm` binary releases are built from git tags.

The current v0.1 distributions pair binary `v0.1.18` with Codex plugin
`v0.1.3` and Claude Code plugin `v0.1.18`. The Codex plugin installer pins that
binary version by default because it uses `paxm setup --integration
codex-plugin` and requires Codex-native `UserPromptSubmit` output for passive
recall.

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

## Plugin Distribution

After the binary release is available:

1. Validate `plugins/paxm-memory/` with the plugin validator and shell syntax
   checks.
2. Confirm `.agents/plugins/marketplace.json` points at the plugin directory.
3. Test installation from the repo marketplace in Codex, including a new task,
   hook trust review, setup, remember/recall, disable, and re-enable.
4. Create the plugin release tag only after the binary compatibility check is
   green. Pin public marketplace updates to the reviewed release tag or commit
   rather than `main`.

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
install.sh
```

Each archive contains the `paxm` binary and the project README.
`install.sh` is uploaded as a release asset so users can install with
`curl -fsSL https://github.com/pax-beehive/paxm/releases/latest/download/install.sh | bash`.

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

Smoke test the installer against the release:

```bash
curl -fsSL https://github.com/pax-beehive/paxm/releases/latest/download/install.sh -o /tmp/paxm-install.sh
PAXM_INSTALL_DIR=/tmp/paxm-install-smoke bash /tmp/paxm-install.sh
/tmp/paxm-install-smoke/paxm version
```

## Self Update

Released binaries can install future releases:

```bash
paxm update --check
paxm update
```

Useful flags:

- `--version v0.1.1`: install a specific release instead of the latest release.
- `--install-path PATH`: install the downloaded binary somewhere other than the
  currently running executable.
- `--force`: reinstall even when the current `paxm version` matches the target
  tag.

`paxm update` verifies the downloaded archive against the release `SHA256SUMS`
before replacing the binary. After a successful in-place install it safely shuts
down the existing hook daemon, which durably seals pending capture state first.
The updater waits for the socket and lock to disappear; the next real hook starts
the updated daemon and resumes delivery. `--check` and `--install-path` do not
stop the current daemon, and a shutdown failure is a warning rather than an
update rollback.
