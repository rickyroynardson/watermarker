# Learning Secrets Manager and ECS injection

Our application already reads configuration from environment variables. ECS can
supply sensitive variables at startup from Secrets Manager, so we do not need a
custom secret-fetching client inside Go or Python.

## What we store and who receives it

| Variable | AWS services receiving it | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | API, consumer, monitor, cleanup | Database connection string, including credentials. |
| `REDIS_URL` | API, consumer, cleanup | Redis connection string, potentially including credentials. |
| `WORKER_API_TOKEN` | API, worker | Authentication for worker cancellation checks. |
| `OIDC_CLIENT_SECRET` | API | Confidential OIDC client credential. |

The frontend gets no server secrets. `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, and
`APP_ORIGIN` are ordinary settings, configured alongside the OIDC secret when
sign-in is enabled. An OIDC secret alone is not sufficient: partial login
configuration deliberately fails API startup.

Read [`secrets.tf`](../infra/terraform/secrets.tf) and
[`outputs.tf`](../infra/terraform/outputs.tf). Terraform creates secret
**containers/metadata**, not secret versions or values. Names use the environment
prefix, for example `watermarker-dev/database_url`. No `secret_string`, secret
value data source, or secret value Terraform input is introduced.

Secrets use Secrets Manager's default AWS-managed encryption key. We do not add
a customer-managed KMS key in this step. If one is introduced, execution roles
also need appropriately scoped `kms:Decrypt` permission.

## Follow startup

1. A task definition contains `secrets: [{name, valueFrom}]`, where `valueFrom`
   is the full secret ARN. `container_services` outputs this list for future AWS
   task definitions. It contains references, not the secret text.
2. ECS fetches each value using the **execution role** before starting the container.
3. The application receives the named environment variable and uses its existing
   configuration code. Its **task role** does not need `GetSecretValue` for this flow.

A separate `runtime-secrets` inline policy permits only `secretsmanager:GetSecretValue`
on that service's required secret ARNs. It does not permit updating or deleting
secrets. The existing image-pull/log policy stays separate.

A real AWS Fargate task also needs network access to Secrets Manager, through
outbound connectivity or an interface endpoint. These policies do not create a
network route. The AWS root still does not create running Fargate tasks.

## Populate AWS values outside Terraform

Apply the reviewed infrastructure plan after configuring its S3 backend. Terraform
outputs `secret_arns` and `container_services` references. Secret containers have
no value yet, so an ECS task referencing them cannot start until they are populated.

Use the AWS Secrets Manager console to add a **plaintext** value to each secret.
Store the full URL/token/client secret as the value, not a JSON object containing
it. Our injection uses the entire value, not a JSON-key selector.

Alternatively, use the AWS CLI with a restricted plaintext file outside the
repository. Authenticate normally via profile/SSO; substitute the actual secret
name and file path:

```sh
chmod 600 /secure/path/database-url.txt
AWS_PROFILE=your-profile aws secretsmanager put-secret-value \
  --region us-east-1 \
  --secret-id watermarker-dev/database_url \
  --secret-string file:///secure/path/database-url.txt \
  --query VersionId --output text
```

Do not type real values directly into CLI arguments, committed scripts, Terraform
variables, or CI logs. The file must contain exactly the intended value (avoid an
unwanted trailing newline). Remove temporary files after use. Population needs a
separate administrative identity authorized for `PutSecretValue`; application
execution roles are deliberately read-only. No CI secret-writing job is added.

The metadata has a 30-day recovery window on deletion. Reusing a deleted secret
name can fail while it is pending deletion; recover the secret deliberately rather
than bypassing that protection. Secrets Manager storage/API calls have AWS charges.

## LocalStack implementation

`make ecs-up` now seeds three **public demo values** into LocalStack Secrets
Manager before Terraform runs: database URL, Redis URL, and worker API token.
It creates or updates them without printing values. These names are local-only
(`watermarker-ecs-local/...`) and are not the AWS root's secret metadata resources.
The helper deliberately overwrites these demo values on each local deployment;
do not use it to manage real or manually customized secrets.

The local root reads only `aws_secretsmanager_secret` metadata, never a secret
version. Task definitions use `secrets` references instead of putting database,
Redis, or token values into the plain `environment` list. Each service receives
only the references it needs. The local web task has an empty secrets list.

Google login remains unconfigured locally, so no real OIDC values are imported
from your `.env`. Compose infrastructure/migrations still use their existing
local demo database configuration. This is an ECS injection exercise; it does
not verify AWS execution-role enforcement.

To test explicitly with OrbStack/Docker running:

```sh
make ecs-up
python3 scripts/test_ecs_local.py
```

The live smoke test verifies web/API access and two images through the pipeline.
Successful database/Redis connections prove those variables reached the services.
The smoke test also calls the worker-only endpoint using the token inside the
worker container, checks that worker receives no database URL, and checks that
the current Terraform state contains neither demo database credentials nor the
demo worker token. No token is printed.

Inspect references, not values:

```sh
docker compose exec -T localstack awslocal secretsmanager list-secrets --query 'SecretList[].{Name:Name,ARN:ARN}'
docker compose exec -T localstack awslocal ecs describe-task-definition --task-definition watermarker-local-api --query 'taskDefinition.containerDefinitions[].secrets'
```

Avoid `get-secret-value` or full `docker inspect` output when collecting diagnostics.
Injected values exist in container environment variables even though task definitions
and new Terraform state snapshots contain only references. Host Docker access can
expose those variables; Secrets Manager is not protection from a compromised host.

Destroying the local ECS Terraform root does not remove CLI-owned demo secrets.
They stay in the emulator until explicitly deleted or its state is reset.

For an ignored LocalStack copy of the AWS metadata root, also add
`secretsmanager = "http://localhost:4566"` to the provider endpoint override
before planning the new resources. Provider and backend endpoints are separate.

## Updates, rotation, and limits

Changing a stored value does not update an already-running container. Redeploy
all affected tasks to fetch the current value. In AWS, after reviewing the change:

```sh
aws ecs update-service --cluster YOUR-CLUSTER --service api --force-new-deployment
```

For shared worker tokens, API and worker must be coordinated; staggered changes
can temporarily cause authentication failures. Database rotation also needs to
change actual database credentials. We do not implement automatic rotation or
multiple accepted worker tokens in this step.

Values are absent from new Terraform plans/state because Terraform never reads
or writes them. `sensitive=true` alone would only hide display output, not remove
values from state. Older snapshots/plans containing previous values do not become
sanitized automatically. Environment values must not be logged by application code.

## Tests and exercises

```sh
terraform -chdir=infra/terraform test
terraform -chdir=infra/ecs-local test
```

Provider mocks check per-service ARN scopes, metadata recovery settings, and local
ECS references with no sensitive variables in the plain environment list. Live
LocalStack tests exercise injection, not AWS IAM or real secret rotation.

Explain why the execution role needs secret access, why web does not, and why a
running container does not see a newly stored value. Trace a database URL from
Secrets Manager through a task definition to `os.Getenv("DATABASE_URL")`.

References: [ECS secret injection](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/secrets-envvar-secrets-manager.html),
[execution roles](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task_execution_IAM_role.html),
[Secrets Manager CLI](https://docs.aws.amazon.com/cli/latest/reference/secretsmanager/put-secret-value.html).
