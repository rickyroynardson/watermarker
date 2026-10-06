terraform {
  required_version = ">= 1.7, < 2.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}

# Deliberately local-only: all used service APIs must point at LocalStack.
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  allowed_account_ids         = ["000000000000"]
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  endpoints {
    ecs            = "http://localhost:4566"
    logs           = "http://localhost:4566"
    sts            = "http://localhost:4566"
    secretsmanager = "http://localhost:4566"
  }
}

variable "image_tag" {
  description = "Unique tag built locally by make ecs-up; a new tag creates a task revision."
  type        = string
  validation {
    condition     = can(regex("^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$", var.image_tag))
    error_message = "Provide a valid Docker image tag."
  }
}

locals {
  service_secrets = {
    api      = toset(["DATABASE_URL", "REDIS_URL", "WORKER_API_TOKEN"])
    consumer = toset(["DATABASE_URL", "REDIS_URL"])
    worker   = toset(["WORKER_API_TOKEN"])
    web      = toset([])
  }
  services = {
    api      = { image = "api", command = ["api"], ports = [{ containerPort = 8080, hostPort = 8080, protocol = "tcp" }] }
    consumer = { image = "api", command = ["consumer"], ports = [] }
    worker   = { image = "worker", command = ["python", "main.py"], ports = [] }
    web      = { image = "web", command = [], ports = [{ containerPort = 8080, hostPort = 5180, protocol = "tcp" }] }
  }
  runtime = {
    APP_ENV                   = "development"
    AWS_REGION                = "us-east-1"
    AWS_ACCESS_KEY_ID         = "test"
    AWS_SECRET_ACCESS_KEY     = "test"
    AWS_SESSION_TOKEN         = ""
    AWS_ENDPOINT_URL          = "http://localhost.localstack.cloud:4566"
    AWS_ENDPOINT_URL_S3       = "http://localhost.localstack.cloud:4566"
    AWS_ENDPOINT_URL_SQS      = "http://localhost.localstack.cloud:4566"
    S3_BUCKET                 = "watermarker"
    SQS_JOBS_QUEUE_URL        = "http://localhost.localstack.cloud:4566/000000000000/watermarker-jobs"
    SQS_RESULTS_QUEUE_URL     = "http://localhost.localstack.cloud:4566/000000000000/watermarker-results"
    SQS_JOBS_DLQ_QUEUE_URL    = "http://localhost.localstack.cloud:4566/000000000000/watermarker-jobs-dlq"
    SQS_RESULTS_DLQ_QUEUE_URL = "http://localhost.localstack.cloud:4566/000000000000/watermarker-results-dlq"
    WORKER_API_URL            = "http://host.docker.internal:8080"
    WORKER_CONCURRENCY        = "1"
    QUOTA_DEMO_ENABLED        = "true"
  }
}

# Read metadata only; scripts/ecs-local.sh creates local demo values before Terraform.
data "aws_secretsmanager_secret" "runtime" {
  for_each = toset(flatten([for names in local.service_secrets : tolist(names)]))
  name     = "watermarker-ecs-local/${lower(each.key)}"
}

resource "aws_ecs_cluster" "local" {
  name = "watermarker-ecs-local"
}

resource "aws_cloudwatch_log_group" "service" {
  for_each          = local.services
  name              = "/ecs/watermarker-local/${each.key}"
  retention_in_days = 7
}

resource "aws_ecs_task_definition" "service" {
  for_each     = local.services
  family       = "watermarker-local-${each.key}"
  network_mode = "bridge"
  # LocalStack emulates this through Docker, without registering an EC2 host.
  requires_compatibilities = ["EC2"]
  container_definitions = jsonencode([merge({
    name         = each.key
    image        = "watermarker-${each.value.image}:${var.image_tag}"
    essential    = true
    memory       = each.key == "worker" ? 1024 : 256
    portMappings = each.value.ports
    stopTimeout  = each.key == "worker" ? 120 : 45
    environment = [for name, value in(each.key == "web" ? { API_UPSTREAM = "host.docker.internal:8080" } : local.runtime) : {
      name = name, value = value
    }]
    secrets = [for name in sort(tolist(local.service_secrets[each.key])) : {
      name = name, valueFrom = data.aws_secretsmanager_secret.runtime[name].arn
    }]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.service[each.key].name
        awslogs-region        = "us-east-1"
        awslogs-stream-prefix = each.key
      }
    }
  }, length(each.value.command) == 0 ? {} : { command = each.value.command })])
}

resource "aws_ecs_service" "service" {
  for_each        = local.services
  name            = each.key
  cluster         = aws_ecs_cluster.local.id
  task_definition = aws_ecs_task_definition.service[each.key].arn
  desired_count   = 1
  launch_type     = "EC2"
  # Fixed host ports require stopping the previous task before starting its replacement.
  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100
}

output "web_url" {
  value = "http://localhost:5180"
}
