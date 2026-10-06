# AWS infrastructure

This Terraform root provisions a two-zone VPC with public/isolated private
subnets and service security groups, one private image bucket, standard jobs/results
queues with individual DLQs, and IAM policies for API, worker, consumer, monitor,
and cleanup. It also provisions two ECR repositories, ECS task/execution roles,
and per-service CloudWatch log groups and Secrets Manager metadata. Resources use the existing application prefixes and environment
variable names. LocalStack continues to use `scripts/localstack/init.sh`.

Read the [networking learning guide](../../docs/infrastructure-networking.md) for
a code walkthrough, traffic flows, LocalStack commands, exercises, and the next
infrastructure steps. The [container and IAM guide](../../docs/infrastructure-containers.md)
covers image publishing, role separation, LocalStack tests, and exercises.
This AWS root does not deploy containers or a load balancer.
For secret population, permissions, and injection, read the
[secrets learning guide](../../docs/infrastructure-secrets.md).
For the optional GitHub OIDC plan role and disabled manual workflow, read the
[GitHub identity learning guide](../../docs/infrastructure-github-oidc.md).
For actual local container execution, see the separate
[LocalStack ECS setup](../../docs/infrastructure-ecs-local.md).

Use Terraform >= 1.10. Provider versions are locked by `.terraform.lock.hcl`.

## Check without AWS credentials

```sh
terraform -chdir=infra/terraform init -backend=false
terraform -chdir=infra/terraform fmt -check -recursive
terraform -chdir=infra/terraform validate
terraform -chdir=infra/terraform test
```

Tests use Terraform's mocked AWS provider. They verify bucket access controls,
staging expiry, queue/DLQ routing, subnet routing, and service permissions without making AWS
calls or deploying resources. CI runs these same checks. Mock tests do not prove
that real AWS accepts a deployment or that live IAM permissions work.

## Plan your AWS environment

```sh
cp infra/terraform/terraform.tfvars.example infra/terraform/terraform.tfvars
```

Edit the account ID, region, a globally unique bucket name, name prefix and exact
frontend origins. Keep credentials out of Terraform variables: authenticate with
your normal AWS profile/SSO or workload identity. The provider rejects an account
that does not match `aws_account_id`. Use a separate directory/state per
environment; the name prefix alone does not separate Terraform state.

```sh
AWS_PROFILE=your-profile terraform -chdir=infra/terraform init -backend-config=backend.s3.tfbackend
AWS_PROFILE=your-profile terraform -chdir=infra/terraform plan -out=aws.tfplan
```

Review the plan before applying it:

```sh
AWS_PROFILE=your-profile terraform -chdir=infra/terraform apply aws.tfplan
terraform -chdir=infra/terraform output -json application_env
terraform -chdir=infra/terraform output -json service_policy_arns
```

This creates AWS resources with normal AWS charges. This repository has no CI
apply job. Existing resources must be imported into state before managing them;
Terraform does not adopt the current LocalStack resources or existing AWS objects
just because their names match.

State uses an S3 backend with native locking. First follow the
[remote state learning guide](../../docs/infrastructure-state.md) to bootstrap
its dedicated bucket and initialize or migrate existing state. Do not run a
fresh apply against already-owned resources without migrating their state.
The bootstrap and local ECS roots retain local state. No backend credentials or
state files belong in Git.

## Connect the application

Use the `application_env` output for `AWS_REGION`, `S3_BUCKET`, and all four queue
URLs. Remove LocalStack endpoint overrides (`AWS_ENDPOINT_URL`,
`AWS_ENDPOINT_URL_S3`, `AWS_ENDPOINT_URL_SQS`) and test credentials when using AWS.
Supply database, Redis, frontend/auth, telemetry, and worker cancellation settings
through the existing configuration. Set `QUOTA_DEMO_ENABLED=false` for a real
non-demo environment. Terraform does not deploy application processes or provide
database, Redis, credentials, or secrets.

The generated service policies are attached to matching ECS task roles.
`container_services` outputs the task/execution role ARNs, repository URLs, and
log groups for future task definitions. Execution roles only pull images and
write logs; application permissions belong on task roles. The API policy allows signed uploads, source promotion,
staging deletion and output downloads; worker permissions allow reading inputs,
writing outputs, consuming jobs and publishing results. The consumer dispatches
jobs and consumes results plus the jobs DLQ; the monitor can only read queue
attributes; cleanup can check bucket versioning and delete application objects.
Bucket-level `ListBucket` permissions let API/worker HEAD requests distinguish
missing objects (404) from forbidden objects (403).

The bucket blocks public access, disables ACLs, uses S3-managed encryption,
and rejects non-HTTPS requests. CORS accepts only the configured frontend
origins. Browser signed POST uploads and GET/HEAD image access are permitted;
CORS does not grant unauthenticated object access. Only `uploads/` expires after
seven days. Keep application cleanup running for `sources/` and `processed/`.
The bucket is deliberately never versioned because cleanup must reclaim bytes,
not create delete markers. Never enable versioning on it outside Terraform.
`force_destroy=false` prevents Terraform from emptying a populated bucket.

Source queues retain messages four days, DLQs fourteen days. Queues use
AWS-managed encryption, 20-second long polling, 120-second visibility, and
`maxReceiveCount=3`. Each DLQ accepts redrive only from its corresponding source.
The application still controls per-message visibility and worker heartbeats.

References: [AWS provider](https://registry.terraform.io/providers/hashicorp/aws/latest/docs),
[Terraform provider mocks](https://developer.hashicorp.com/terraform/language/tests/mocking),
[S3 HEAD permissions](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html).
