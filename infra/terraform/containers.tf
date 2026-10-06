locals {
  # The Go image includes API, consumer, monitor, and cleanup binaries.
  service_images = { api = "api", worker = "worker", consumer = "api", monitor = "api", cleanup = "api" }
  ecs_trust = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = {
        StringEquals = { "aws:SourceAccount" = var.aws_account_id }
        ArnLike      = { "aws:SourceArn" = "arn:aws:ecs:${var.aws_region}:${var.aws_account_id}:*" }
      }
    }]
  })
}

resource "aws_ecr_repository" "app" {
  for_each             = toset(values(local.service_images))
  name                 = "${var.name_prefix}/${each.key}"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = false
  image_scanning_configuration { scan_on_push = true }
  encryption_configuration { encryption_type = "AES256" }
}

resource "aws_cloudwatch_log_group" "service" {
  for_each          = local.service_images
  name              = "/ecs/${var.name_prefix}/${each.key}"
  retention_in_days = 14
}

resource "aws_iam_role" "task" {
  for_each           = local.service_images
  name               = "${var.name_prefix}-${each.key}-task"
  assume_role_policy = local.ecs_trust
}

resource "aws_iam_role_policy_attachment" "task" {
  for_each   = local.service_images
  role       = aws_iam_role.task[each.key].name
  policy_arn = aws_iam_policy.service[each.key].arn
}

resource "aws_iam_role" "execution" {
  for_each           = local.service_images
  name               = "${var.name_prefix}-${each.key}-execution"
  assume_role_policy = local.ecs_trust
}

resource "aws_iam_role_policy" "execution" {
  for_each = local.service_images
  name     = "image-pull-and-logs"
  role     = aws_iam_role.execution[each.key].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      # ECR authentication does not support repository-scoped resource permissions.
      { Effect = "Allow", Action = ["ecr:GetAuthorizationToken"], Resource = ["*"] },
      {
        Effect   = "Allow"
        Action   = ["ecr:BatchCheckLayerAvailability", "ecr:GetDownloadUrlForLayer", "ecr:BatchGetImage"]
        Resource = [aws_ecr_repository.app[each.value].arn]
      },
      {
        Effect   = "Allow"
        Action   = ["logs:CreateLogStream", "logs:PutLogEvents"]
        Resource = ["${trimsuffix(aws_cloudwatch_log_group.service[each.key].arn, ":*")}:log-stream:*"]
      }
    ]
  })
}
