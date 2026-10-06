# Learning ECR and ECS roles

Read [`containers.tf`](../infra/terraform/containers.tf) alongside this guide.
This step prepares images, permissions, and logs for ECS. It does not create ECS
task definitions, services, running containers, or a deployment pipeline.

## The resources we added

| Resource | Purpose |
| --- | --- |
| Two private ECR repositories | Store the Go image (`api`) and Python image (`worker`). |
| Five ECS task roles | Give API, worker, consumer, monitor, and cleanup their own application permissions. |
| Five ECS execution roles | Allow ECS to pull the appropriate image and write that service's logs. |
| Five CloudWatch log groups | Separate service logs, retained for 14 days. |

The existing Go Dockerfile builds every command in `apps/api/cmd`. API, consumer,
monitor, and cleanup can use the same image with different container commands.
Sharing an image does not mean sharing an IAM role. The frontend deployment will
be addressed separately; we do not add a web repository in this step.

## Task role versus execution role

When the worker starts, there are two different actors:

1. ECS uses the **execution role** to authenticate to ECR, pull the image, and
   send container stdout/stderr to CloudWatch through the future `awslogs` driver.
2. Python code uses the **task role** through the AWS SDK to read source images,
   consume jobs, write outputs, and send result messages.

The API's task role cannot poll the worker queue. The monitor's task role can
read queue attributes but cannot delete messages. Existing policies in `iam.tf`
are attached to matching task roles by `aws_iam_role_policy_attachment.task`.
The execution policy is separate and cannot access application S3 objects/SQS.

Later ECS task definitions will consume `container_services` output fields:
`task_role_arn` as `taskRoleArn`, `execution_role_arn` as `executionRoleArn`,
`repository_url` plus an image version as `image`, and `log_group_name` for
`awslogs-group`. The roles alone do not start anything or deliver logs yet.

The Go AWS SDK and Python boto3 support ECS-provided temporary credentials.
Remove LocalStack test access keys, profiles, and endpoint overrides from AWS
containers so their normal credential chains can use the task role. Do not put
AWS credentials into an image or Terraform variables. Application secrets are now prepared through Secrets Manager metadata and scoped
execution-role read policies. See the [secrets guide](infrastructure-secrets.md).

## Trust policy versus permission policy

A trust policy answers **who may assume this role**. Ours trusts
`ecs-tasks.amazonaws.com`, constrained to the configured account and region by
`aws:SourceAccount` and `aws:SourceArn`. The ECS source ARN ends with `*`, as AWS
does not currently support restricting this condition to a specific ECS cluster.

A permission policy answers **what the role may do** after assumption.
Execution roles have three statements:

- `ecr:GetAuthorizationToken` on `*`: this API does not support repository-scoped
  resources. This is the intentional exception, not permission to pull every image.
- Three image-pull actions on only the service's repository ARN.
- `logs:CreateLogStream` and `logs:PutLogEvents` on streams in only its log group.

Terraform creates the log groups; execution roles cannot create arbitrary groups
or push images. A developer or later CI identity needs separate ECR push
permissions. Likewise, the identity deploying ECS will need appropriately scoped
`iam:PassRole` permissions. We do not grant those to application roles.

## Image versions and retention

Repositories use immutable tags. Publishing `api:<commit>` again fails if that
tag already exists, preventing accidental replacement of a deployed version.
Use a new release tag for a new image; avoid a moving `latest` tag. A digest such
as `repository@sha256:...` identifies the exact artifact even more explicitly.

Images use AES256 encryption and basic scan-on-push. Inspect scan findings before
choosing a release; requesting a scan is not a guarantee an image has no issues.
Scanning availability/results must be checked on AWS, not inferred from a mock.

`force_delete=false` prevents Terraform from deleting repositories with images.
No image-expiration policy is added yet: silently deleting images used for
rollback is undesirable. Add retention rules once a release/rollback policy is
chosen. Stored images and CloudWatch logs incur AWS charges; logs expire after
14 days. Destroying a log group deletes its retained logs.

## Verify locally without AWS

```sh
terraform -chdir=infra/terraform init -backend=false
terraform -chdir=infra/terraform fmt -check -recursive
terraform -chdir=infra/terraform validate
terraform -chdir=infra/terraform test
```

The new `tests/containers.tftest.hcl` checks repository settings, role trust,
service-policy attachments, pull/log permission scopes, and outputs. CI runs it
automatically. Mock tests do not execute IAM authorization or pull images.

For LocalStack, follow the isolated-directory setup in the
[networking guide](infrastructure-networking.md). Copy the updated `.tf` files
into the same local directory and keep its existing state and override. Add
these endpoints to `local_override.tf` alongside EC2, IAM, S3/S3 Control, SQS,
and STS:

```hcl
ecr  = "http://localhost:4566"
logs = "http://localhost:4566"
```

Then review and apply a new local plan:

```sh
terraform -chdir=infra/terraform/.terraform/localstack plan -var-file=local.tfvars -out=local.tfplan
terraform -chdir=infra/terraform/.terraform/localstack apply local.tfplan
terraform -chdir=infra/terraform/.terraform/localstack output -json container_services

docker compose exec -T localstack awslocal ecr describe-repositories
docker compose exec -T localstack awslocal iam get-role --role-name watermarker-tf-local-worker-task
docker compose exec -T localstack awslocal iam list-attached-role-policies --role-name watermarker-tf-local-worker-task
docker compose exec -T localstack awslocal iam get-role-policy --role-name watermarker-tf-local-worker-execution --policy-name image-pull-and-logs
docker compose exec -T localstack awslocal logs describe-log-groups --log-group-name-prefix /ecs/watermarker-tf-local/
```

This checks resource creation and policy documents. Real ECS credential delivery,
IAM enforcement, image pulls, and log delivery require an AWS runtime test.

## Build and publish on AWS when ready

These are manual commands for real AWS, after applying the reviewed AWS plan.
They require Docker, AWS CLI, jq, authenticated AWS access, and ECR push
permissions. They publish images; Terraform does not run them automatically.
Set `AWS_PROFILE` if needed and use the same region as Terraform.

```sh
export AWS_REGION=us-east-1
API_REPOSITORY=$(terraform -chdir=infra/terraform output -json container_repositories | jq -r '.api')
WORKER_REPOSITORY=$(terraform -chdir=infra/terraform output -json container_repositories | jq -r '.worker')
ECR_REGISTRY=${API_REPOSITORY%%/*}
IMAGE_TAG=$(git rev-parse HEAD)

aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$ECR_REGISTRY"
docker build --platform linux/amd64 -t "$API_REPOSITORY:$IMAGE_TAG" apps/api
docker build --platform linux/amd64 -t "$WORKER_REPOSITORY:$IMAGE_TAG" apps/worker
docker push "$API_REPOSITORY:$IMAGE_TAG"
docker push "$WORKER_REPOSITORY:$IMAGE_TAG"
```

Commit the source first so the tag actually identifies what you built. For a
local experiment with uncommitted changes, choose a unique explicit tag instead.
The commands target x86-64; later ECS task CPU architecture must match. If a tag
already exists, reuse the published artifact or choose a new version rather than
turning off immutability. These commands do not deploy the application.

## Exercises and next step

1. Compare worker and monitor task permissions in `iam.tf`.
2. Find why the consumer can pull the Go image but cannot read source objects.
3. Explain why giving S3 permissions to the execution role would not fix a
   worker application's `AccessDenied`.
4. Inspect the trust policy and distinguish its principal from permission resources.
5. Inspect the execution policy and explain the one wildcard resource.

Next: provide outbound network connectivity, then define ECS tasks/services and
connect database, Redis, secrets, and load balancing. Before shared deployment,
configure remote Terraform state with locking. CI image publishing using GitHub
OIDC can follow once the AWS deployment identity is defined.

Official references:

- [ECS task roles](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task-iam-roles.html)
- [ECS execution roles](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task_execution_IAM_role.html)
- [ECR permissions](https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazonelasticcontainerregistry.html)
- [Pushing images](https://docs.aws.amazon.com/AmazonECR/latest/userguide/docker-push-ecr-image.html)
- [Immutable image tags](https://docs.aws.amazon.com/AmazonECR/latest/userguide/image-tag-mutability.html)
- [Image scanning](https://docs.aws.amazon.com/AmazonECR/latest/userguide/image-scanning.html)
