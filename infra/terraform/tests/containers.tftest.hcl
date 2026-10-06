mock_provider "aws" {
  mock_resource "aws_iam_policy" {
    defaults = { arn = "arn:aws:iam::123456789012:policy/test-policy" }
  }
  mock_data "aws_availability_zones" {
    defaults = { names = ["us-east-1a", "us-east-1b"] }
  }
}

variables {
  aws_account_id = "123456789012"
  bucket_name    = "watermarker-containers-test"
  app_origins    = ["https://watermarker.example.com"]
}

run "container_permissions" {
  command = apply

  assert {
    condition = (toset(keys(aws_ecr_repository.app)) == toset(["api", "worker"]) &&
      alltrue([for repo in aws_ecr_repository.app : repo.image_tag_mutability == "IMMUTABLE" && !repo.force_delete && repo.image_scanning_configuration[0].scan_on_push && repo.encryption_configuration[0].encryption_type == "AES256"]) &&
    alltrue([for group in aws_cloudwatch_log_group.service : group.retention_in_days == 14]))
    error_message = "Images must use immutable tags, scanning, encryption, and safe deletion; logs must have bounded retention."
  }

  assert {
    condition = alltrue([for role in concat(values(aws_iam_role.task), values(aws_iam_role.execution)) :
      length(jsondecode(role.assume_role_policy).Statement) == 1 &&
      jsondecode(role.assume_role_policy).Statement[0].Principal.Service == "ecs-tasks.amazonaws.com" &&
      jsondecode(role.assume_role_policy).Statement[0].Action == "sts:AssumeRole" &&
      jsondecode(role.assume_role_policy).Statement[0].Condition.StringEquals["aws:SourceAccount"] == var.aws_account_id &&
      jsondecode(role.assume_role_policy).Statement[0].Condition.ArnLike["aws:SourceArn"] == "arn:aws:ecs:${var.aws_region}:${var.aws_account_id}:*"
    ])
    error_message = "Only ECS tasks from the configured account and region may assume these roles."
  }

  assert {
    condition = alltrue([for service, image in local.service_images :
      aws_iam_role_policy_attachment.task[service].role == aws_iam_role.task[service].name &&
      aws_iam_role_policy_attachment.task[service].policy_arn == aws_iam_policy.service[service].arn &&
      aws_iam_role_policy.execution[service].role == aws_iam_role.execution[service].id &&
      length(jsondecode(aws_iam_role_policy.execution[service].policy).Statement) == 3 &&
      jsondecode(aws_iam_role_policy.execution[service].policy).Statement[0].Action == ["ecr:GetAuthorizationToken"] &&
      jsondecode(aws_iam_role_policy.execution[service].policy).Statement[0].Resource == ["*"] &&
      toset(jsondecode(aws_iam_role_policy.execution[service].policy).Statement[1].Action) == toset(["ecr:BatchCheckLayerAvailability", "ecr:GetDownloadUrlForLayer", "ecr:BatchGetImage"]) &&
      jsondecode(aws_iam_role_policy.execution[service].policy).Statement[1].Resource == [aws_ecr_repository.app[image].arn] &&
      toset(jsondecode(aws_iam_role_policy.execution[service].policy).Statement[2].Action) == toset(["logs:CreateLogStream", "logs:PutLogEvents"]) &&
      jsondecode(aws_iam_role_policy.execution[service].policy).Statement[2].Resource == ["${trimsuffix(aws_cloudwatch_log_group.service[service].arn, ":*")}:log-stream:*"] &&
      output.container_services[service].task_role_arn == aws_iam_role.task[service].arn &&
      output.container_services[service].execution_role_arn == aws_iam_role.execution[service].arn &&
      output.container_services[service].repository_url == aws_ecr_repository.app[image].repository_url
    ])
    error_message = "Application permissions belong on task roles; execution roles may only authenticate, pull the matching image, and write their own logs."
  }
  assert {
    condition = (toset(keys(aws_secretsmanager_secret.app)) == toset(["DATABASE_URL", "REDIS_URL", "OIDC_CLIENT_SECRET", "WORKER_API_TOKEN"]) &&
      local.service_secrets.api == toset(["DATABASE_URL", "REDIS_URL", "OIDC_CLIENT_SECRET", "WORKER_API_TOKEN"]) &&
      local.service_secrets.worker == toset(["WORKER_API_TOKEN"]) &&
      local.service_secrets.consumer == toset(["DATABASE_URL", "REDIS_URL"]) &&
      local.service_secrets.monitor == toset(["DATABASE_URL"]) &&
      local.service_secrets.cleanup == toset(["DATABASE_URL", "REDIS_URL"]) &&
      alltrue([for secret in aws_secretsmanager_secret.app : secret.recovery_window_in_days == 30]) &&
      alltrue([for service, names in local.service_secrets :
        aws_iam_role_policy.secrets[service].role == aws_iam_role.execution[service].id &&
        length(jsondecode(aws_iam_role_policy.secrets[service].policy).Statement) == 1 &&
        jsondecode(aws_iam_role_policy.secrets[service].policy).Statement[0].Action == ["secretsmanager:GetSecretValue"] &&
        jsondecode(aws_iam_role_policy.secrets[service].policy).Statement[0].Resource == [for name in sort(tolist(names)) : aws_secretsmanager_secret.app[name].arn] &&
        output.container_services[service].secrets == [for name in sort(tolist(names)) : { name = name, valueFrom = aws_secretsmanager_secret.app[name].arn }]
    ]))
    error_message = "Execution roles must read only their service's runtime secrets, and outputs must contain references only."
  }
}

override_resource {
  target = aws_iam_policy.service["api"]
  values = { arn = "arn:aws:iam::123456789012:policy/watermarker-api" }
}

override_resource {
  target = aws_iam_policy.service["worker"]
  values = { arn = "arn:aws:iam::123456789012:policy/watermarker-worker" }
}

override_resource {
  target = aws_iam_policy.service["consumer"]
  values = { arn = "arn:aws:iam::123456789012:policy/watermarker-consumer" }
}

override_resource {
  target = aws_iam_policy.service["monitor"]
  values = { arn = "arn:aws:iam::123456789012:policy/watermarker-monitor" }
}

override_resource {
  target = aws_iam_policy.service["cleanup"]
  values = { arn = "arn:aws:iam::123456789012:policy/watermarker-cleanup" }
}
