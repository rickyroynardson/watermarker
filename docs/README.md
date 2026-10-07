# Documentation

Start with the [project README](../README.md) for setup and a first batch.

## Application

| Guide | What it covers |
| --- | --- |
| [Architecture](architecture.md) | Service responsibilities, processing flow and failures |
| [Containers](containers.md) | Full local stack, demo account, scaling, monitoring and cleanup |
| [API](../apps/api/README.md) | Authentication, endpoints, quotas, cancellation and retries |
| [Worker](../apps/worker/README.md) | Processing, delivery retries and concurrency |
| [Web](../apps/web/README.md) | Browser setup, uploads and live updates |
| [Observability](../observability/README.md) | Logs, metrics, tracing, dashboards and alerts |
| [Load tests](../scripts/load/README.md) | Repeatable fixtures and worker performance reports |
| [CI](ci.md) | Automated checks and the paused image publishing workflow |

## PostgreSQL exercises

The query, lock and backup labs create disposable databases with synthetic data.
They do not connect to your application database.

| Topic | Command from the repository root |
| --- | --- |
| [Batch history and query plans](database-query-tuning.md) | `make query-lab` |
| [Quota aggregation](database-quota-tuning.md) | `make quota-query-lab` |
| [Cleanup indexes and row locks](database-cleanup-tuning.md) | `make cleanup-query-lab` |
| [Quota lock contention](database-lock-contention.md) | `make quota-lock-lab` |
| [Logical backup and restore](backup-recovery.md) | `make backup-recovery-lab` |
| [Physical backup and PITR](postgresql-pitr.md) | `make pitr-recovery-lab` |

For the running application, read [pool configuration](database-pool-configuration.md),
[pool monitoring](database-pool-observability.md), and
[transaction/lock monitoring](database-activity-observability.md).

## Infrastructure exercises

Follow these in order when exploring deployment:

1. [Terraform setup](../infra/terraform/README.md) and [networking](infrastructure-networking.md).
2. [Container repositories and IAM roles](infrastructure-containers.md).
3. [LocalStack ECS execution](infrastructure-ecs-local.md).
4. [Secrets and injection](infrastructure-secrets.md).
5. [Terraform remote state](infrastructure-state.md) and its [bootstrap root](../infra/state-bootstrap/README.md).
6. [GitHub Actions identity for AWS](infrastructure-github-oidc.md).

LocalStack and mocked tests cover different parts of the configuration. They do
not establish that a real AWS deployment works. Follow each guide's prerequisites
and limits before applying infrastructure.
