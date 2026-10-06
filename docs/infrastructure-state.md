# Learning Terraform remote state and locking

Terraform state maps resource addresses such as `aws_s3_bucket.images` to real
resource IDs. It also records attributes and can contain secrets. It is not just
a cache: losing it can make Terraform propose duplicate resources or lose track
of resources it owns. Keep state and saved plans out of Git.

## What we added

- `infra/state-bootstrap` creates a dedicated private, encrypted, versioned S3
  bucket with public access blocked, ACLs disabled, and HTTPS required.
- `infra/terraform/backend.tf` selects the S3 backend. The actual bucket/key are
  supplied during initialization, not hardcoded into the infrastructure root.
- `backend.s3.tfbackend.example` enables `use_lockfile=true`. Terraform >=1.10
  is required. No DynamoDB table is needed for native S3 locking.
- The local ECS root keeps local state. Do not connect it to an AWS backend.

The state bucket is separate from the image bucket: application cleanup must
never delete infrastructure state. State versioning keeps earlier snapshots;
the image bucket deliberately remains unversioned for actual file deletion.
No lifecycle expiry is configured for state versions. Review retention later
with an explicit recovery policy.

## Why bootstrap is separate

The S3 backend is initialized before Terraform evaluates resources. It cannot
create its own bucket during that initialization. Create the bucket using the
bootstrap root's local state, then initialize the application root against it.
Keep a secure backup of the bootstrap state too. Do not put its state in the
application root's key; each root/environment must have its own ownership records.

`prevent_destroy=true` deliberately blocks deleting the state bucket through
Terraform. It is not an AWS security control and does not stop console/CLI
deletions. `force_destroy=false` adds protection against emptying a populated
bucket. Versioning is recovery help, not a replacement for restricted access or
an independent backup.

## Bootstrap on AWS

Authenticate using your AWS profile/SSO. This step creates an AWS bucket; review
its plan before applying. No credential values belong in the variable file.

```sh
cp infra/state-bootstrap/terraform.tfvars.example infra/state-bootstrap/terraform.tfvars
# Edit the account, region and globally unique bucket name.
AWS_PROFILE=your-profile terraform -chdir=infra/state-bootstrap init
AWS_PROFILE=your-profile terraform -chdir=infra/state-bootstrap plan -out=bootstrap.tfplan
AWS_PROFILE=your-profile terraform -chdir=infra/state-bootstrap apply bootstrap.tfplan
```

Keep a secure copy of `infra/state-bootstrap/terraform.tfstate` after applying.
Then configure the application root:

```sh
cp infra/terraform/backend.s3.tfbackend.example infra/terraform/backend.s3.tfbackend
```

Edit bucket/account/region to match bootstrap. Choose one key per environment,
for example `environments/dev/terraform.tfstate`. This guide uses the default
Terraform workspace; workspace prefixes require additional IAM resources.
Backend configuration cannot use `var.*` because initialization happens first.

## Migrate existing local state safely

Stop other Terraform operations. Run from the **same directory that owns the
existing local state**; do not use a fresh checkout to adopt existing resources.
Before initializing the new backend, copy `terraform.tfstate` and any backup to
a secure location outside the repository. If no local state exists, this is a
fresh backend initialization rather than a migration of deployed resources.

```sh
AWS_PROFILE=your-profile terraform -chdir=infra/terraform init -migrate-state -backend-config=backend.s3.tfbackend
```

Read the migration prompt and confirm only if the source and destination are
correct. `-migrate-state` copies ownership records; it does not recreate the
application resources. Do not use `-reconfigure` to migrate: it changes backend
configuration without copying existing state.

After migration:

```sh
AWS_PROFILE=your-profile terraform -chdir=infra/terraform state list
AWS_PROFILE=your-profile terraform -chdir=infra/terraform plan -lock-timeout=60s
```

Compare resource addresses with the old state and review the plan. Do not apply
unexpected replacements. Retain the secure backup until migration and resource
ownership are verified. Do not keep operating an old checkout with local state.
Other collaborators initialize against the same backend configuration after
migration; they do not import or recreate these resources.

CI still uses `init -backend=false` and provider mocks; it needs no AWS credentials.
A first real plan/apply now requires configured remote state. Existing LocalStack
copies can keep using `init -backend=false` until deliberately migrating locally.

## Locking and least privilege

Terraform conditionally creates `<key>.tflock` before writing state and removes
it when the operation finishes. Another operation must wait or fail rather than
write concurrently. Use `-lock-timeout=60s` for normal shared deployments. Do not
routinely use `-lock=false`. Backend credentials and provider credentials are
configured separately, even though both can use the same AWS profile.

The deployment identity needs the following backend permissions, in addition to
permissions for the resources it provisions. Substitute your exact bucket/key:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:ListBucket"],
      "Resource": "arn:aws:s3:::YOUR-STATE-BUCKET"
    },
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject"],
      "Resource": "arn:aws:s3:::YOUR-STATE-BUCKET/environments/dev/terraform.tfstate"
    },
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"],
      "Resource": "arn:aws:s3:::YOUR-STATE-BUCKET/environments/dev/terraform.tfstate.tflock"
    }
  ]
}
```

Deleting the state object is not needed. Grant these permissions to the future
Terraform deployment identity, not API/worker task roles. Bootstrap needs broader
bucket-management permissions; it is a separate administrative operation.
The policy example is not attached automatically because no CI deployment
identity has been created yet. Backend profiles/secrets should remain outside
`.tfbackend` files: Terraform caches backend config and may include it in plans.

## Recovery

If a process crashes, first ensure no operation is still active. Inspect the lock
owner/ID before using `terraform force-unlock LOCK_ID`. Never remove a colleague's
active lock. Unlocking does not recover missing/corrupt state.

For recovery, stop writers and download a known-good S3 object version to a
secure file. Compare lineage, serial, resource IDs and actual infrastructure
before considering `terraform state push`; restoring an old snapshot blindly
can drop ownership of newer resources. State-push safeguards should not be
bypassed routinely. S3 versioning preserves prior objects but access to those
versions requires separately authorized recovery permissions.

## LocalStack exercise

Services may stay stopped while you run mock checks. For a live backend exercise,
start the base infrastructure explicitly with `make up`; this does not start ECS
or app services. LocalStack with `PERSISTENCE=0` loses the bucket/state on restart,
so it is an emulator exercise, not durable remote storage.

Use an isolated ignored copy of bootstrap:

```sh
mkdir -p infra/state-bootstrap/.terraform/localstack
cp infra/state-bootstrap/*.tf infra/state-bootstrap/.terraform/localstack/
cp infra/state-bootstrap/.terraform.lock.hcl infra/state-bootstrap/.terraform/localstack/
```

Create `local_override.tf` there with the provider override from the
[networking guide](infrastructure-networking.md), keeping S3, S3 Control and STS
endpoints at `http://localhost:4566`. Replace its bucket-policy override with:

```hcl
resource "aws_s3_bucket_policy" "https" {
  count  = 0
  bucket = aws_s3_bucket.state.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [] })
}
```

The policy is never created (`count=0`); HTTP is allowed only for the emulator.
Create `local.tfvars` containing account `000000000000`, region `us-east-1`, and
bucket `watermarker-tf-local-state`. Initialize, plan, and apply that copy using
`-var-file=local.tfvars`, as in earlier guides.

For the existing ignored application root copy, copy the new `backend.tf` into
it and create `local.s3.tfbackend`:

```hcl
bucket                      = "watermarker-tf-local-state"
key                         = "environments/local/terraform.tfstate"
region                      = "us-east-1"
encrypt                     = true
use_lockfile                = true
allowed_account_ids         = ["000000000000"]
use_path_style              = true
skip_credentials_validation = true
skip_metadata_api_check     = true
endpoints = {
  s3  = "http://localhost:4566"
  sts = "http://localhost:4566"
}
```

Provider endpoint overrides do **not** configure the backend's separate client.
After securely backing up any local state:

```sh
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN= AWS_PROFILE= terraform -chdir=infra/terraform/.terraform/localstack init -migrate-state -backend-config=local.s3.tfbackend
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN= AWS_PROFILE= terraform -chdir=infra/terraform/.terraform/localstack plan -var-file=local.tfvars -lock-timeout=60s
```

During a pending `terraform apply` confirmation (without auto-approve), inspect
S3 for the `.tflock` object from another terminal. Try a second plan with
`-lock-timeout=2s`: it should fail while the first writer owns the lock. Answer
`no` in the first terminal, then retry the plan and inspect released locking.
Do not delete lock objects manually. This tests emulator locking behavior;
AWS IAM enforcement and durable recovery must be tested on AWS.

## Checks and reading

```sh
terraform -chdir=infra/state-bootstrap init -backend=false
terraform -chdir=infra/state-bootstrap validate
terraform -chdir=infra/state-bootstrap test
```

Tests check state-bucket security settings using a mocked provider. They do not initialize
an S3 backend or prove live lock contention. CI checks this root too.

References: [S3 backend and permissions](https://developer.hashicorp.com/terraform/language/backend/s3),
[backend migration](https://developer.hashicorp.com/terraform/language/backend),
[state locking](https://developer.hashicorp.com/terraform/language/state/locking),
[LocalStack Terraform](https://docs.localstack.cloud/aws/connecting/infrastructure-as-code/terraform/).
