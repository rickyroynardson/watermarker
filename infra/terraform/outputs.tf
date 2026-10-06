output "application_env" {
  description = "Non-secret AWS settings for the application; supply database, Redis, auth and worker-token settings separately."
  value = {
    AWS_REGION                = var.aws_region
    S3_BUCKET                 = aws_s3_bucket.images.id
    SQS_JOBS_QUEUE_URL        = aws_sqs_queue.pipeline["jobs"].url
    SQS_RESULTS_QUEUE_URL     = aws_sqs_queue.pipeline["results"].url
    SQS_JOBS_DLQ_QUEUE_URL    = aws_sqs_queue.dlq["jobs"].url
    SQS_RESULTS_DLQ_QUEUE_URL = aws_sqs_queue.dlq["results"].url
  }
}

output "service_policy_arns" {
  description = "Attach each policy to the matching service's IAM task role."
  value       = { for service, policy in aws_iam_policy.service : service => policy.arn }
}

output "network" {
  description = "Network IDs for the future load balancer and container services."
  value = {
    vpc_id             = aws_vpc.app.id
    public_subnet_ids  = { for zone, subnet in aws_subnet.public : zone => subnet.id }
    private_subnet_ids = { for zone, subnet in aws_subnet.private : zone => subnet.id }
    security_group_ids = { for service, group in aws_security_group.service : service => group.id }
  }
}

output "container_repositories" {
  description = "Private ECR repository URLs; build and push images separately from Terraform."
  value       = { for image, repository in aws_ecr_repository.app : image => repository.repository_url }
}

output "container_services" {
  description = "Image repository, role ARNs, and log group for future ECS task definitions."
  value = { for service, image in local.service_images : service => {
    repository_url     = aws_ecr_repository.app[image].repository_url
    task_role_arn      = aws_iam_role.task[service].arn
    execution_role_arn = aws_iam_role.execution[service].arn
    log_group_name     = aws_cloudwatch_log_group.service[service].name
    secrets = [for name in sort(tolist(local.service_secrets[service])) : {
      name = name, valueFrom = aws_secretsmanager_secret.app[name].arn
    }]
  } }
}

output "secret_arns" {
  description = "Secret metadata only; write values with Secrets Manager separately."
  value       = { for name, secret in aws_secretsmanager_secret.app : name => secret.arn }
}
