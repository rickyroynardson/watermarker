# Run the app through LocalStack ECS

This local learning setup runs API, consumer, worker, and web as four ECS
services. LocalStack creates Docker containers through OrbStack/Docker. Compose
runs only the dependencies (Postgres, Redis, LocalStack) and the one-shot migration.
There is no EC2 VM to SSH into; access the web at **http://localhost:5180**.

## Start and test

Start OrbStack/Docker first. Stop host API/web processes and any Compose app
containers using ports 8080/5180. If the app overlay is running, use `make app-down`
first (retains volumes). Keep your existing root `.env` with the LocalStack token.
This requires a LocalStack plan that supports ECS, Terraform >=1.7, and Docker.

```sh
make ecs-up
```

This builds the existing three images, starts healthy dependencies, runs database
migrations, tags the images with a unique local version, then asks you to review
and confirm Terraform's deployment plan. Open http://localhost:5180 after apply.

No Google credentials are copied into Terraform state. Login is intentionally
unconfigured in this local example; the UI supports existing API keys. For a
complete pipeline smoke test using a temporary key and bundled image fixtures:

```sh
python3 scripts/test_ecs_local.py
```

The generated test account uses the existing Pro plan so custom free-plan limits
do not reject the bundled fixtures. The test checks the web proxy and runs two images through uploads, API, database,
consumer/outbox, SQS, worker/S3, and results processing. It revokes its generated
key afterward; the test user/batch remain for inspection and normal cleanup.
The test requires Node.js for our existing load script. For manual browser use,
use an existing key from your local database (keys are not recoverable from their
hashes). Google sign-in and account purchases are not part of this local ECS test.

## What the code does

- `docker-compose.ecs.yml` mounts the Docker socket into LocalStack and connects
  spawned task containers to `watermarker-ecs`, the dependencies' Docker network.
  This gives LocalStack control over your local Docker engine; use this overlay
  for trusted local development only.
- `scripts/ecs-local.sh` builds/tags images and runs migrations **before** deployment.
  Terraform does not build images or order application readiness across services.
- `infra/ecs-local/main.tf` defines a cluster, four task definitions/services, and
  four CloudWatch log groups. Each service requests one task. New image tags create
  task-definition revisions. Desired count is orchestration intent; inspect actual
  task/container status and run the smoke test to verify availability.
- API publishes port 8080; web publishes 5180. Worker and consumer expose no ports.
  These Docker mappings may bind all host interfaces: this is a local development
  setup, not a public deployment. Do not expose it to untrusted networks.
- Web proxies `/api` to `host.docker.internal:8080`; workers use the same host route
  for cancellation checks. The Nginx image's native template support makes the
  upstream configurable. Compose retains the default `api:8080` upstream.
- API/consumer use Postgres/Redis DNS names on the shared network. LocalStack's
  `localhost.localstack.cloud` alias is reachable from both browser and containers,
  preserving signed S3 URLs. The existing init script owns the bucket/queues.
- Local AWS credentials are demo values; database/Redis URLs and worker token
  are injected from LocalStack Secrets Manager. Read the
  [secrets guide](infrastructure-secrets.md) for the startup flow. This root does not
  use the AWS deployment root's IAM roles, VPC, or ECR repositories; it exercises
  actual local ECS container execution without pretending to verify AWS security.

A **task definition** describes how to start a container: image, command,
environment, ports, memory, and log driver. A **task** is one running copy.
A **service** declares how many tasks should exist. A **cluster** groups them.
Services have separate task definitions even when API/consumer share the Go image.

Fixed ports prevent old and new copies from running simultaneously. Deployment
settings stop the old task before starting its replacement, so updates have
intentional downtime. Do not scale API/web above one with these fixed mappings.

## Inspect and stop

```sh
docker compose exec -T localstack awslocal ecs list-services --cluster watermarker-ecs-local
docker compose exec -T localstack awslocal ecs list-tasks --cluster watermarker-ecs-local
docker compose exec -T localstack awslocal logs filter-log-events --log-group-name /ecs/watermarker-local/worker
docker ps --filter name=ls-ecs
docker logs <task-container-name>
make ecs-down
```

Destroy removes this root's ECS services/tasks and log groups. It leaves Compose
infrastructure, Postgres data, and the init-script bucket/queues intact. Tagged
Docker images also remain; remove unused versions deliberately through OrbStack.
Do not remove Terraform state while its services still exist.

If LocalStack is recreated with persistence disabled, emulator resources disappear
and Terraform must refresh/recreate them. The helper idempotently recreates
the named local cluster before refresh, because the AWS provider retries service
reads against a missing cluster rather than immediately forgetting those records. LocalStack persistence also does not
substitute for PostgreSQL or image backups. Avoid operating the Compose app stack
and this ECS deployment simultaneously against the same queues.

## LocalStack versus AWS

This root is deliberately separate from `infra/terraform`, with a locked AWS
provider pointed only at LocalStack endpoints and fake account ID. It uses Docker
bridge networking and LocalStack's EC2 launch-type emulation, with no registered
EC2 hosts. It is **not** an AWS Fargate configuration.

AWS Fargate will need `awsvpc`, real task/execution roles, ECR image URIs, outbound
network connectivity, private service discovery/load balancing, secrets, and
managed or explicitly hosted Postgres/Redis. Its network isolation and IAM
credential delivery cannot be verified by this local example.

Before AWS deployment, also move shared state to an encrypted backend with locking.
Then add release deployment and rollback using immutable image versions.

## Checks and exercises

```sh
terraform -chdir=infra/ecs-local init -backend=false -lockfile=readonly
terraform -chdir=infra/ecs-local fmt -check -recursive
terraform -chdir=infra/ecs-local validate
terraform -chdir=infra/ecs-local test
```

CI runs these configuration checks; they do not run LocalStack ECS. The live smoke
test is separate. Try changing an image tag and inspect the new task revision;
stop a task and observe whether LocalStack restores the requested service count.
Compare `docker ps` to ECS `list-tasks` rather than assuming an ACTIVE service
means every container is healthy.

References: [LocalStack ECS](https://docs.localstack.cloud/aws/services/ecs/),
[ECS task definitions](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task_definitions.html),
[ECS services](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/ecs_services.html).
