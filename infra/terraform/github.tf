# Opt-in: bootstrap this role using your own AWS login before enabling the workflow.
resource "aws_iam_openid_connect_provider" "github" {
  count          = var.github_plan == null ? 0 : 1
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
}

resource "aws_iam_role" "github_plan" {
  count = var.github_plan == null ? 0 : 1
  name  = "${var.name_prefix}-github-plan"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRoleWithWebIdentity"
      Principal = { Federated = aws_iam_openid_connect_provider.github[0].arn }
      Condition = { StringEquals = {
        "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
        "token.actions.githubusercontent.com:sub" = var.github_plan.subject
      } }
    }]
  })
}

resource "aws_iam_role_policy" "github_plan" {
  count = var.github_plan == null ? 0 : 1
  name  = "terraform-plan"
  role  = aws_iam_role.github_plan[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "InfrastructureMetadata"
        Effect = "Allow"
        # These reads refresh the resources in this root. No secret values,
        # queue messages, image objects, image pulls, or deployment writes.
        Action = [
          "ec2:Describe*",
          "ecr:DescribeRepositories", "ecr:ListTagsForResource",
          "logs:DescribeLogGroups", "logs:ListTagsForResource",
          "secretsmanager:DescribeSecret", "secretsmanager:GetResourcePolicy",
          "iam:GetRole", "iam:ListRolePolicies", "iam:GetRolePolicy",
          "iam:ListAttachedRolePolicies", "iam:GetPolicy", "iam:GetPolicyVersion",
          "iam:ListPolicyVersions", "iam:GetOpenIDConnectProvider",
          "sqs:GetQueueAttributes", "sqs:GetQueueUrl", "sqs:ListQueueTags"
        ]
        Resource = "*"
      },
      {
        Sid      = "ImageBucketMetadata"
        Effect   = "Allow"
        Action   = ["s3:ListBucket", "s3:GetBucket*", "s3:GetEncryptionConfiguration", "s3:GetLifecycleConfiguration", "s3:ListTagsForResource"]
        Resource = ["arn:aws:s3:::${var.bucket_name}"]
      },
      {
        Sid      = "StateBucket"
        Effect   = "Allow"
        Action   = ["s3:ListBucket"]
        Resource = ["arn:aws:s3:::${var.github_plan.state_bucket}"]
      },
      {
        Sid      = "ReadState"
        Effect   = "Allow"
        Action   = ["s3:GetObject"]
        Resource = ["arn:aws:s3:::${var.github_plan.state_bucket}/${var.github_plan.state_key}"]
      },
      {
        Sid      = "StateLock"
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
        Resource = ["arn:aws:s3:::${var.github_plan.state_bucket}/${var.github_plan.state_key}.tflock"]
      }
    ]
  })
}

output "github_plan_role_arn" {
  description = "Set AWS_PLAN_ROLE_ARN in GitHub after bootstrapping; null when disabled."
  value       = one(aws_iam_role.github_plan[*].arn)
}
