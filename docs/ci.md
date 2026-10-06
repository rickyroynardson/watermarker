# Continuous integration

[The CI workflow](../.github/workflows/ci.yml) runs on pushes to `main`, pull
requests and manual runs from GitHub's Actions tab. Feature branches need an open
PR for automatic checks, avoiding duplicate push and PR runs. New commits cancel
older runs for the same branch or pull request.

Four jobs run in parallel:

- **Go tests:** formatting, unit tests and Docker-backed integration tests with
  the race detector. The Ubuntu runner has a C compiler, so CGO stays enabled.
- **Python tests:** locked worker dependencies and standard-library unit tests.
- **Frontend checks:** lint, formatting, tests, TypeScript and Vite build, plus
  the existing load-script tests.
- **Terraform checks:** formatting, provider initialization, validation and mocked
  tests. It needs no AWS credentials and deploys no AWS resources.

After the three application jobs pass, **Container smoke test** builds the three application images
and runs `scripts/test_container_stack.py`. It checks migrations, proxying,
uploads, worker processing, results, SSE and signed downloads using disposable
services. Failures print service logs, and the script removes its resources.

No repository secrets, Google login credentials or LocalStack Pro token are
required. Integration and smoke tests use community LocalStack. Dependencies
are cached by the setup actions; images are built on each fresh runner.

## Image publishing

GHCR publishing is currently paused: its login/push steps and `packages: write`
permission are commented out. CI still runs all tests, image builds and the
container smoke check. Uncomment those steps and permission to restore the
publishing behavior described below.

After all checks and the container smoke test pass on a push to `main`, the same
container job publishes the three tested images to GitHub Container Registry
(GHCR). It tags the existing local images rather than rebuilding them.
Pull requests, other branches and manual runs only verify changes.

Images are tagged with the full commit SHA:

```text
ghcr.io/rickyroynardson/watermarker-api:<commit-sha>
ghcr.io/rickyroynardson/watermarker-worker:<commit-sha>
ghcr.io/rickyroynardson/watermarker-web:<commit-sha>
```

The API image also contains consumer, monitor, cleanup and migration executables.
Published images target Linux amd64, matching the Ubuntu runner. No `latest` tag
is published; choose a specific commit for deployment and a previous one for
rollback. For an immutable reference, use the image digest reported by GHCR.

Publishing uses GitHub's automatic `GITHUB_TOKEN` with `packages: write` in the
container job; no personal access token or additional repository secret is needed.
Package visibility is configured separately on GitHub. Private packages require
authentication when pulling from a deployment host; make them public if anonymous
pulls are desired. See [GitHub's registry guide](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).

The job summary lists successfully published references. Publishing is per image,
so a failure can leave only some images uploaded; check that all three exist for
the selected commit before deployment. Rerun the failed job to retry publishing.
This workflow does not deploy anything.

## Pull request workflow

GitHub branch protection for `main` requires a pull request, the four existing application CI checks
passing against the latest base branch, and resolved review conversations.
It applies to administrators too; force pushes and branch deletion are disabled.
Required approvals are zero so a solo maintainer can merge their own PR. Raise
that count to one when another reviewer joins. These settings live on GitHub,
not in this workflow file. The new Terraform check must be added separately
to required checks after its first GitHub run if it should also block merging.

For each change:

```sh
git switch -c feature/describe-change
# Make and test the change, then commit it.
git push -u origin feature/describe-change
gh pr create --base main
```

Review the diff and CI results before merging. If the branch is behind `main`,
update it and wait for the checks again. After merging, return to `main` and
pull the latest changes before starting the next branch.

Terraform configuration in `infra/terraform` also runs formatting, initialization,
validation, and provider-mocked tests in CI. This job uses no AWS credentials and
never applies resources to AWS. See the [infrastructure guide](../infra/terraform/README.md).

## Provider checksums across Mac and CI

CI initializes providers with `-lockfile=readonly`. Commit verified unpacked
package (`h1:`) checksums for both `darwin_arm64` (Apple Silicon development)
and `linux_amd64` (GitHub's Ubuntu runner). The official archive (`zh:`) hashes
can verify a download, but read-only initialization cannot add a missing platform's
unpacked checksum before validation checks the cached package.

When intentionally updating a provider, run this for each Terraform root:

```sh
terraform -chdir=infra/terraform providers lock -platform=darwin_arm64 -platform=linux_amd64
terraform -chdir=infra/ecs-local providers lock -platform=darwin_arm64 -platform=linux_amd64
terraform -chdir=infra/state-bootstrap providers lock -platform=darwin_arm64 -platform=linux_amd64
```

Review and commit the lock-file changes. Keep checksum verification enabled;
provider caches and state files stay uncommitted. If a correctly locked package
is damaged, remove only the affected provider cache and initialize again. Do not
delete the whole `.terraform` directory: it also contains backend configuration
and our ignored LocalStack working copies.

Reference: [Terraform provider locking](https://developer.hashicorp.com/terraform/cli/commands/providers/lock).

## Optional AWS plan workflow

`terraform-plan.yml` is manual-only and skips unless `AWS_PLAN_ENABLED=true`
and the selected branch is `main`. It uses temporary GitHub OIDC credentials
with a metadata-read/state-lock role; it cannot apply or publish images.
No GitHub variables or AWS resources have been configured by adding the file.
The existing CI Terraform job remains credential-free. Follow the
[OIDC learning guide](infrastructure-github-oidc.md) for mock tests, trust-policy
concepts, limitations, and future account setup.
