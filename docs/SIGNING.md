# Code signing (free, via SignPath Foundation)

The Windows agent is distributed as an `.exe`. Unsigned, Windows SmartScreen and
Defender warn on (or block) it. Because this project is open source, it qualifies
for **free** code signing from the **SignPath Foundation** OSS program. This
document is the one-time setup; after it, every tagged release is signed
automatically by `.github/workflows/release.yml`.

There is no code to change — the release workflow already contains the signing
step. It stays inert (publishing an unsigned binary with a warning) until the
SignPath organization variables below are present, then it signs automatically.

## 1. Apply to the SignPath Foundation (one-time, manual)

1. Go to https://signpath.org/ and apply to the **Foundation (open source)** tier
   for this repository (`Kiragu-Maina/pos-agent`, MIT licensed).
2. Install the **SignPath** GitHub App on the repo when prompted.
3. Wait for SignPath to review and approve the project. (This is a human review;
   it can take a few days.)

## 2. Configure the project in SignPath (after approval)

In the SignPath web console:

1. Note your **Organization ID** (Settings) and your **GitHub Actions connector
   URL** (Integrations → GitHub Actions).
2. Create a **Project** — use the slug `pos-agent`.
3. Add an **Artifact Configuration** for a single Authenticode-signed PE (an
   `.exe`); note its slug (e.g. `exe`).
4. Add a **Signing Policy** for releases; note its slug (e.g. `release-signing`).
5. Create a **CI/CD API token** for the GitHub Actions user.

## 3. Add the secrets and variables to GitHub

Repo → **Settings → Secrets and variables → Actions**.

**Secret:**

| Name | Value |
|---|---|
| `SIGNPATH_API_TOKEN` | the CI/CD API token from step 2.5 |

**Variables:**

| Name | Value (example) |
|---|---|
| `SIGNPATH_CONNECTOR_URL` | your GitHub Actions connector URL |
| `SIGNPATH_ORGANIZATION_ID` | your SignPath organization GUID |
| `SIGNPATH_PROJECT_SLUG` | `pos-agent` |
| `SIGNPATH_SIGNING_POLICY_SLUG` | `release-signing` |
| `SIGNPATH_ARTIFACT_CONFIG_SLUG` | `exe` |

The workflow only runs the signing step when `SIGNPATH_ORGANIZATION_ID` is set,
so nothing breaks before this is configured.

## 4. Cut a release

```
git tag v1.0.0
git push origin v1.0.0
```

(or run the **release** workflow manually via the Actions tab and enter a
version). The workflow builds the agent, sends it to SignPath, gets back the
signed `.exe`, and attaches it to a GitHub Release.

## Notes

- Signing gives the binary your **publisher identity** (no more "Unknown
  publisher"). SmartScreen reputation still builds over downloads/time — that is
  expected for all certificates now, EV included.
- For supply-chain safety you can pin the SignPath action to a commit SHA instead
  of `@v2.2`.
- The signed `.exe` from the release is what should be served at
  `pos.alkenacode.dev/download/pos.exe` going forward.
