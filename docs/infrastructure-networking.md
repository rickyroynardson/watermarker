# Learning AWS networking with Terraform

This is the networking step of our infrastructure roadmap. Read
[`network.tf`](../infra/terraform/network.tf) alongside this guide. Terraform
creates the network and security groups; it does not deploy containers yet.

## What each resource does

| Concept | Our implementation | Why it exists |
| --- | --- | --- |
| VPC | `aws_vpc.app`, `10.20.0.0/16` | Defines a private address space for our deployment. |
| Availability zone (AZ) | First two sorted available standard AZs in the selected region | Allows services to spread across two locations within a region. |
| Public subnet | `10.20.0.0/24`, `10.20.1.0/24` | Future internet-facing load balancer placement. |
| Private subnet | `10.20.10.0/24`, `10.20.11.0/24` | Future API and worker placement. Currently isolated. |
| Internet gateway | `aws_internet_gateway.app` | Connects the VPC's public routes to the internet. |
| Route table | One public and one private table, explicitly associated with their subnets | Chooses where packets go based on destination address. |
| Security group | Separate groups for load balancer, API, worker | Controls allowed connections at the attached network interface. |

A `/16` leaves 16 bits for addresses; `/24` leaves 8. AWS reserves five addresses
in each subnet. `cidrsubnet(vpc_cidr, 8, index)` splits our `/16` into `/24`
blocks. Indices 0 and 1 are public; 10 and 11 are private. Change the VPC CIDR
before creating resources if it overlaps a network you will later connect to.
Changing a deployed CIDR or AZ can require resource replacement: review the plan.

`for_each` uses stable keys `a` and `b` to create the two subnets of each kind.
Those keys are Terraform labels, not guaranteed AWS zone suffixes. Actual AZs
come from the provider's data source. Two zones alone do not make an application
highly available; containers and data services must also be deployed accordingly.

## Follow a request

The intended path when we add compute is:

```text
Browser --HTTPS:443--> load balancer in public subnets
                         |
                         +--HTTP:8080--> API in private subnets

Worker in private subnets --HTTPS:443--> SQS / S3
```

The load balancer will terminate TLS using a certificate. No load balancer,
certificate, or HTTPS listener exists in this step. The API listens on port 8080
in our application code. Its ingress rule references the load balancer's security
group rather than allowing every address in the VPC. The load balancer's egress
also permits only the API group on 8080, including health checks.

Workers poll SQS; SQS does not open a connection into the worker. Workers need
no inbound rule. API and workers allow outbound TCP 443, but that rule alone
does not provide a network route. Postgres, Redis, cancellation calls to the API,
and telemetry connectivity need explicit rules when their deployment is added.
Do not deploy our existing full application assuming these rules cover it yet.

Security groups are stateful: replies to allowed connections do not need a
separate reverse rule. A security-group reference selects attached interfaces;
it neither grants AWS API permissions nor routes packets. IAM authorizes S3/SQS
operations; routes provide reachability; security groups permit connections.
All three must be correct.

## Public versus private is about routes

The public table has `0.0.0.0/0 -> internet gateway`. Both tables also have the
AWS-managed local VPC route, allowing internal routing. Private subnets have no
default route to the internet.

We disable automatic public IPv4 assignment in *both* kinds of subnet. A public
subnet does not automatically expose everything in it: internet access also
requires a public address where appropriate and allowed security-group rules.
A future internet-facing load balancer manages its own public addresses.

Private subnets are currently **isolated**. Before running ECS there we must
provide access to image registries, logging, S3/SQS, and any external APIs:

- A NAT gateway supports outbound internet access from private addresses. It
  adds hourly and traffic charges; availability needs consideration per AZ.
- VPC endpoints provide access to supported AWS services without an internet
  route. Gateway endpoints and interface endpoints have different costs and
  configuration. They do not provide general internet access for Google OIDC.

We defer that choice to the compute step, where actual dependencies determine
which routes/endpoints we need. No NAT gateway, endpoints, public IP allocations,
or compute instances are created by this network configuration.

## Check without deploying

From the repository root:

```sh
terraform -chdir=infra/terraform fmt -check -recursive
terraform -chdir=infra/terraform validate
terraform -chdir=infra/terraform test
```

Run `terraform -chdir=infra/terraform init -backend=false` first if necessary.
`tests/network.tftest.hcl` checks subnet ranges/zones, route associations, and
security-group boundaries with a mocked provider. CI includes these tests.
Mocks verify configuration, not actual packet delivery or AWS permissions.

## Test resource creation with LocalStack

Use the **same isolated directory and state** as the earlier LocalStack test.
Do not delete its state or recreate a different directory to manage existing
resources. Copy the updated root files without overwriting the local override:

```sh
mkdir -p infra/terraform/.terraform/localstack
cp infra/terraform/*.tf infra/terraform/.terraform/localstack/
cp infra/terraform/.terraform.lock.hcl infra/terraform/.terraform/localstack/
```

For first-time setup, create `local_override.tf` in that directory:

```hcl
provider "aws" {
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  s3_use_path_style           = true
  endpoints {
    ec2       = "http://localhost:4566"
    s3        = "http://localhost:4566"
    s3control = "http://localhost:4566"
    sqs       = "http://localhost:4566"
    iam       = "http://localhost:4566"
    sts       = "http://localhost:4566"
  }
}

# Omit the TLS enforcement policy for HTTP LocalStack only.
# Required attributes are retained for editors that do not merge overrides.
resource "aws_s3_bucket_policy" "https" {
  count  = 0
  bucket = aws_s3_bucket.images.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Deny", Principal = "*", Action = "s3:*"
      Resource = [aws_s3_bucket.images.arn, "${aws_s3_bucket.images.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
}
```

If the override already exists, add only `ec2` to its endpoint block and ensure
`s3control` is present. Terraform uses the EC2 API for VPC networking. Missing
endpoints can send requests to real AWS. `localhost` works from the host terminal;
`localstack` is the Docker network hostname.

For first-time setup, create `local.tfvars` in that directory:

```hcl
aws_account_id = "000000000000"
aws_region     = "us-east-1"
name_prefix    = "watermarker-tf-local"
bucket_name    = "watermarker-tf-local-images"
app_origins    = ["http://localhost:5173", "http://127.0.0.1:5173"]
```

With LocalStack running:

```sh
terraform -chdir=infra/terraform/.terraform/localstack init -backend=false
terraform -chdir=infra/terraform/.terraform/localstack plan -var-file=local.tfvars -out=local.tfplan
terraform -chdir=infra/terraform/.terraform/localstack apply local.tfplan
terraform -chdir=infra/terraform/.terraform/localstack output -json network
```

Inspect the resulting topology:

```sh
docker compose exec -T localstack awslocal ec2 describe-vpcs --filters Name=tag:Name,Values=watermarker-tf-local-vpc
docker compose exec -T localstack awslocal ec2 describe-subnets --filters Name=tag:Project,Values=watermarker-tf-local
docker compose exec -T localstack awslocal ec2 describe-route-tables --filters Name=tag:Project,Values=watermarker-tf-local
docker compose exec -T localstack awslocal ec2 describe-security-groups --filters Name=tag:Project,Values=watermarker-tf-local
```

Check that public subnets share the internet route, private subnets do not have
it, and the API ingress source is the load balancer group. LocalStack tests API
resource behavior; these commands do **not** prove real AWS network isolation,
TLS handling, or application availability.

Remove the managed local resources when finished:

```sh
terraform -chdir=infra/terraform/.terraform/localstack destroy -var-file=local.tfvars
```

Empty the image bucket first if it contains files; `force_destroy` stays false.
Do not use the LocalStack overrides or test credentials for an AWS deployment.

## Exercises and next steps

1. Explain why adding an internet gateway alone does not make a subnet public.
2. Find the rules that allow load balancer health checks to reach port 8080.
3. Explain why allowing worker egress on 443 still cannot reach SQS today.
4. Change an API rule in a temporary branch and see whether the mock test catches it.
5. Next: add ECS task roles, image delivery, and the required outbound network
   connectivity. Then deploy API/worker tasks, databases/Redis, and the load balancer.
6. Before shared AWS deployments: move state to an encrypted remote backend with
   locking. Later: automate deployment using GitHub OIDC instead of static keys.

Official learning references:

- [VPC fundamentals](https://docs.aws.amazon.com/vpc/latest/userguide/how-it-works.html)
- [Subnet sizing and reserved addresses](https://docs.aws.amazon.com/vpc/latest/userguide/subnet-sizing.html)
- [Route tables](https://docs.aws.amazon.com/vpc/latest/userguide/subnet-route-tables.html)
- [Internet gateways](https://docs.aws.amazon.com/vpc/latest/userguide/VPC_Internet_Gateway.html)
- [Security groups](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-security-groups.html)
- [NAT gateways](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-nat-gateway.html)
- [VPC endpoints](https://docs.aws.amazon.com/vpc/latest/privatelink/vpc-endpoints.html)
- [Fargate networking](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/fargate-task-networking.html)
- [ECS task roles](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task-iam-roles.html)
