# Continuous integration

[The CI workflow](../.github/workflows/ci.yml) runs on pushes, pull requests and
manual runs from GitHub's Actions tab. New commits cancel older runs for the same
branch or pull request.

Three jobs run in parallel:

- **Go tests:** formatting, unit tests and Docker-backed integration tests with
  the race detector. The Ubuntu runner has a C compiler, so CGO stays enabled.
- **Python tests:** locked worker dependencies and standard-library unit tests.
- **Frontend checks:** lint, formatting, tests, TypeScript and Vite build, plus
  the existing load-script tests.

After those pass, **Container smoke test** builds the three application images
and runs `scripts/test_container_stack.py`. It checks migrations, proxying,
uploads, worker processing, results, SSE and signed downloads using disposable
services. Failures print service logs, and the script removes its resources.

No repository secrets, Google login credentials or LocalStack Pro token are
required. Integration and smoke tests use community LocalStack. Dependencies
are cached by the setup actions; images are built on each fresh runner.

## Image publishing

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

GitHub branch protection for `main` requires a pull request, all four CI checks
passing against the latest base branch, and resolved review conversations.
It applies to administrators too; force pushes and branch deletion are disabled.
Required approvals are zero so a solo maintainer can merge their own PR. Raise
that count to one when another reviewer joins. These settings live on GitHub,
not in this workflow file.

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
