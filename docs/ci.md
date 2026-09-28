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

This workflow verifies changes without publishing images or deploying anything.
To require successful CI before merging, enable branch protection in GitHub and
require these four checks after the workflow's first run.
