# CLIProxyAPI Fork Provenance and Governance

This repository is the maintained `marlon-costa-dc/CLIProxyAPI` fork of the MIT-licensed `router-for-me/CLIProxyAPI`. It also preserves MIT-licensed provider work originating from the former `router-for-me/CLIProxyAPIPlus` history, including the snapshot at commit `0c48ef58` (2026-04-17), before that repository was removed and its successor adopted the SSPL.

## Why this fork exists

- The former `router-for-me/CLIProxyAPIPlus` repository was deleted on 2026-04-17.
- Its successor, `CLIProxyAPIBusiness`, is SSPL-licensed and is not a source for this MIT fork.
- Plus providers (`codebuddy`, `copilot`, `cursor`, `gitlab`, `iflow`, `kilo`, `kiro`) do **not** exist in the still-alive `router-for-me/CLIProxyAPI` (original MIT). They live only here.
- Our MIT snapshot predates the rebrand; MIT rights do not retroactively revoke.

## Authority and integration

`router-for-me/CLIProxyAPI` is the upstream authority for the original project's architecture and behavior. This fork's `main` branch is the authority for the maintained downstream delta, CI, documentation, integration, and releases. Upstream changes are reconciled into fork `main` through reviewed pull requests; upstream `main` is not used as the fork's integration branch.

## Remote roles

- `origin`: `marlon-costa-dc/CLIProxyAPI`; fetch and push target for maintained fork branches and `main`.
- `upstream`: `router-for-me/CLIProxyAPI`; read-only source for upstream reconciliation. Do not push fork work here.
- `cliplus`: `kaitranntt/CLIProxyAPIPlus`; reference remote only. Import code only after provenance and MIT-compatible licensing are verified in review.
- `ravens`: `Ravens2121/CLIProxyAPIPlus`; reference remote only under the same provenance and licensing requirement.

Remote names describe roles, not trust. No reference remote may bypass review, provenance checks, or the fork's `main` integration path.

## Upstream synchronization

Daily GitHub Action (`.github/workflows/upstream-sync.yml`) merges from `router-for-me/CLIProxyAPI` (original, still MIT, still public). Plus-only provider directories are guarded by `.gitattributes merge=ours` — upstream can never touch them.

- **Clean merge** → pushes a dedicated branch, opens a `main`-targeting reconciliation PR, and dispatches read-only build/test validation for that exact head.
- **Conflict or post-resolution preparation/validation failure** → creates or updates one automation-owned issue labeled `upstream-sync-blocked`. Its stable ownership marker and current tag/SHA generation are stored in the body; each update records conflict paths, branch/PR, and workflow run in a comment. Failures before an upstream target can be resolved remain visible on the workflow run because no safe tracker generation exists yet.
- **Successful validation or an already-merged target** → closes only the automation-owned tracker when its current SHA exactly matches the validated target or is proven contained in `main`. Stale runs and unrelated labeled issues are left untouched.
- Tracker mutations from preparation and validation share a `queue: max` concurrency group. Queue order is not trusted: every edit or close re-reads the issue, verifies the `app/github-actions` author, and enforces monotonic tag/exact-SHA state.
- Manual trigger: Actions → "Upstream Sync" → Run workflow.

## Legal boundary

- Any code from `router-for-me/CLIProxyAPIBusiness` — SSPL would infect this fork and any downstream user of CCS. Do not copy, cherry-pick, or look at it as reference.
- Fixes to the 7 plus-only providers must be self-authored or sourced from MIT-compatible contributions only.

The preserved code remains subject to the license attached to its source history. Do not copy, cherry-pick, or use SSPL `CLIProxyAPIBusiness` code as implementation reference. New or imported provider work must be self-authored or have verified MIT-compatible provenance.

## Releases

Releases are owned by [`.github/workflows/release.yaml`](.github/workflows/release.yaml) and [`.goreleaser.yml`](.goreleaser.yml). The workflow invokes GoReleaser for an approved tag and publishes the artifacts defined by that configuration. A documentation, synchronization, or ordinary merge does not publish a release.

Release inputs must come from reviewed fork `main`, pass the workflow's required checks, and follow the version/tag contract encoded by those owners. Change release behavior at those canonical files rather than documenting parallel commands or defaults here.

## Related documentation

- [Project README](README.md)
- [SDK usage](docs/sdk-usage.md)
- [SDK advanced configuration](docs/sdk-advanced.md)
- [Release workflow](.github/workflows/release.yaml)
- [GoReleaser configuration](.goreleaser.yml)
