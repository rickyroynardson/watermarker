# GitHub Actions → AWS with OIDC

This feature is prepared for learning without an AWS account. Nothing is
provisioned now. `github_plan` defaults to `null`; the manual workflow skips
unless `AWS_PLAN_ENABLED=true` and the selected branch is `main`.
Existing CI still runs Terraform mock tests without AWS credentials.

## How authentication works

1. GitHub grants the job `id-token: write`, allowing it to request a signed
   identity token. This permission does not grant AWS access.
2. `configure-aws-credentials` sends that token to AWS STS using
   `AssumeRoleWithWebIdentity`.
3. AWS checks GitHub's signature, the `sts.amazonaws.com` audience, and the
   exact repository/main-branch subject in the role's trust policy.
4. STS returns temporary credentials. Terraform uses those to read the backend
   and infrastructure, then the credentials expire.

The trust policy answers **who can assume the role**. The inline permissions
policy answers **what that role can do**. This uses the same OIDC identity
concept as app sign-in, but the identity is a workflow job rather than a user;
no application session or Google login is involved.

## Walk through the code

- `infra/terraform/variables.tf`: optional `github_plan` object. Validation
  rejects wildcard subjects, other branches, and unsafe state keys.
- `infra/terraform/github.tf`: GitHub identity provider, plan role, trust policy,
  and permissions. No cached certificate thumbprint is needed for GitHub.
- `.github/workflows/terraform-plan.yml`: manual dispatch, opt-in gate, input
  checks, temporary authentication, backend initialization, and plan only.
- `infra/terraform/tests/github.tftest.hcl`: mocks check the disabled default,
  exact trust conditions, state/lock boundaries, and rejected subjects.

Infrastructure reads cover EC2 networking, ECR repository metadata, CloudWatch
log-group metadata, IAM configuration, Secrets Manager metadata, and SQS queue
attributes/tags. These metadata reads currently use account-wide resources;
this is a learning scope, not per-resource isolation. Bucket configuration reads
are limited to the image bucket. `ListBucket` also permits listing image keys
and is needed for the provider's bucket existence check. The role cannot read secret values, image
objects, queue messages, or log events, push images, deploy resources, or
write the state file. It can read the one state object and create/delete its
`.tflock` object, because even a plan must acquire Terraform's lock.
State contents themselves are sensitive: backend access is a privileged capability.
There is no saved plan artifact, PR comment, or apply job. Plans appear in the
Actions log; keep secrets out of Terraform values and configuration.

## Checks you can run now

```sh
terraform -chdir=infra/terraform init -backend=false -input=false -lockfile=readonly
terraform -chdir=infra/terraform fmt -check -recursive
terraform -chdir=infra/terraform validate
terraform -chdir=infra/terraform test
```

Mocks verify configuration contracts, not GitHub token issuance, STS acceptance,
AWS IAM enforcement, or provider refresh permissions. Those need a real account
later. LocalStack is not a substitute for validating this external trust chain.
No Docker services are required for these checks.

## Connect a real account later

Do not enable the workflow yet. First obtain an AWS account and authenticate
locally with SSO/profile credentials. Follow `infrastructure-state.md` to create
the state bucket and initialize/migrate the application root's S3 backend.

Set `github_plan` in the ignored `infra/terraform/terraform.tfvars`:

```hcl
github_plan = {
  subject      = "repo:rickyroynardson/watermarker:ref:refs/heads/main"
  state_bucket = "your-existing-state-bucket"
  state_key    = "environments/dev/terraform.tfstate"
}
```

**Verify the exact subject before using this example.** GitHub repositories
created on/after July 15, 2026 can include immutable owner/repository IDs:
`repo:OWNER@OWNER_ID/REPO@REPO_ID:ref:refs/heads/main`. Older repositories can opt
into that format. Both formats are supported by validation. Confirm your
repository's OIDC subject configuration, or inspect only the decoded `sub`
claim in a controlled main-branch job; never print the raw token. Custom subject
templates are not supported here. Keep this workflow without a GitHub
`environment:`: environment jobs use a different subject format.

There can be only one GitHub OIDC provider for this URL in an AWS account.
If it already exists, coordinate its ownership and import it rather than
creating a duplicate. This root is suitable for the first learning environment;
shared-account/multiple-environment setups should manage the provider centrally.

```sh
# Only if an existing provider should be managed by this root:
AWS_PROFILE=your-profile terraform -chdir=infra/terraform import \
  'aws_iam_openid_connect_provider.github[0]' \
  arn:aws:iam::YOUR_ACCOUNT_ID:oidc-provider/token.actions.githubusercontent.com

AWS_PROFILE=your-profile terraform -chdir=infra/terraform plan -out=aws.tfplan
# Review the entire plan: this root also provisions the other infrastructure.
AWS_PROFILE=your-profile terraform -chdir=infra/terraform apply aws.tfplan
terraform -chdir=infra/terraform output -raw github_plan_role_arn
```

The workflow cannot bootstrap its own role or state bucket. That first apply
must happen with your local identity. The role intentionally cannot apply.
Use the same backend, prefix, bucket, origins, and subject in local and CI
configuration to avoid unintended plan differences.

In GitHub **Settings → Secrets and variables → Actions → Variables**, set:

| Variable | Value |
| --- | --- |
| `AWS_ACCOUNT_ID` | Your 12-digit account ID |
| `AWS_REGION` | Same region as the root and state bucket |
| `AWS_PLAN_ROLE_ARN` | `github_plan_role_arn` output |
| `TF_STATE_BUCKET` | Existing state bucket |
| `TF_STATE_KEY` | Exact backend key, e.g. `environments/dev/terraform.tfstate` |
| `TF_NAME_PREFIX` | Same `name_prefix` as local configuration |
| `TF_IMAGE_BUCKET` | Same `bucket_name` as local configuration |
| `TF_APP_ORIGINS` | JSON array, e.g. `["https://app.example.com"]` |
| `GITHUB_OIDC_SUBJECT` | Exact subject used in `github_plan` |
| `AWS_PLAN_ENABLED` | `true`, only after all setup is complete |

No static AWS access key secrets are required. Run **Actions → AWS Terraform
plan → Run workflow**, selecting `main`. Authentication should resolve to the
configured account/role. A plan can show changes but cannot apply them. If
refresh fails with AccessDenied, identify the specific metadata API and adjust
only that read permission; do not add AdministratorAccess. Set the enabled
variable to `false` to stop future runs (existing STS sessions remain valid until
expiry). Other workflows on main can request this role too: the default subject
restricts repository/branch, not the workflow filename. Protect main and review
workflow edits accordingly. PRs and other branches have no access under this
trust policy. Terraform uses the default workspace and a single explicit key.
The backend example uses SSE-S3; customer-managed KMS would require additional
scoped decrypt permissions, which are not included.

## Learning exercises

- Explain why `id-token: write` alone cannot deploy AWS infrastructure.
- Inspect the mocked trust policy and explain why another branch is rejected.
- Identify the only two write actions and the exact object they can affect.
- Explain why the ECS task role, ECS execution role, and GitHub plan role are
  separate identities.
- Later, confirm a main-branch plan succeeds and another branch is skipped;
  validate AccessDenied boundaries before expanding permissions.

References: [GitHub OIDC with AWS](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-aws),
[official credentials action](https://github.com/aws-actions/configure-aws-credentials),
[Terraform OIDC provider](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_openid_connect_provider),
[S3 backend permissions](https://developer.hashicorp.com/terraform/language/backend/s3#permissions-required).
