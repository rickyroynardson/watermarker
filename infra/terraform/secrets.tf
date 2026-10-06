locals {
  service_secrets = {
    api      = toset(["DATABASE_URL", "REDIS_URL", "OIDC_CLIENT_SECRET", "WORKER_API_TOKEN"])
    consumer = toset(["DATABASE_URL", "REDIS_URL"])
    worker   = toset(["WORKER_API_TOKEN"])
    monitor  = toset(["DATABASE_URL"])
    cleanup  = toset(["DATABASE_URL", "REDIS_URL"])
  }
}

# Metadata only. Write values outside Terraform to keep them out of state and plans.
resource "aws_secretsmanager_secret" "app" {
  for_each                = toset(flatten([for secrets in local.service_secrets : tolist(secrets)]))
  name                    = "${var.name_prefix}/${lower(each.key)}"
  description             = "ECS runtime value for ${each.key}; populated separately from Terraform"
  recovery_window_in_days = 30
}

# ECS fetches injected values before startup using the execution role, not the task role.
resource "aws_iam_role_policy" "secrets" {
  for_each = local.service_secrets
  name     = "runtime-secrets"
  role     = aws_iam_role.execution[each.key].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["secretsmanager:GetSecretValue"]
      Resource = [for name in sort(tolist(each.value)) : aws_secretsmanager_secret.app[name].arn]
    }]
  })
}
