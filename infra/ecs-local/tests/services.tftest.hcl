mock_provider "aws" {}
variables { image_tag = "test-version" }
run "local_runtime" {
  command = apply
  assert {
    condition = (toset(keys(aws_ecs_service.service)) == toset(["api", "consumer", "worker", "web"]) &&
      alltrue([for service in aws_ecs_service.service : service.desired_count == 1 && service.deployment_minimum_healthy_percent == 0 && service.deployment_maximum_percent == 100]) &&
      jsondecode(aws_ecs_task_definition.service["web"].container_definitions)[0].portMappings[0].hostPort == 5180 &&
      jsondecode(aws_ecs_task_definition.service["api"].container_definitions)[0].portMappings[0].hostPort == 8080 &&
    alltrue([for name in ["worker", "consumer"] : length(jsondecode(aws_ecs_task_definition.service[name].container_definitions)[0].portMappings) == 0]))
    error_message = "Deploy exactly four local services, exposing only web/API and stopping old tasks before replacement."
  }
  assert {
    condition = (alltrue([for task in aws_ecs_task_definition.service : endswith(jsondecode(task.container_definitions)[0].image, ":test-version")]) &&
      local.runtime.WORKER_API_URL == "http://host.docker.internal:8080" &&
      local.runtime.AWS_ENDPOINT_URL == "http://localhost.localstack.cloud:4566" &&
    jsondecode(aws_ecs_task_definition.service["web"].container_definitions)[0].environment == [{ name = "API_UPSTREAM", value = "host.docker.internal:8080" }])
    error_message = "Use versioned local images and reachable API/LocalStack endpoints."
  }
  assert {
    condition = alltrue([for service, names in local.service_secrets :
      jsondecode(aws_ecs_task_definition.service[service].container_definitions)[0].secrets == [for name in sort(tolist(names)) : { name = name, valueFrom = data.aws_secretsmanager_secret.runtime[name].arn }] &&
      alltrue([for env in jsondecode(aws_ecs_task_definition.service[service].container_definitions)[0].environment : !contains(["DATABASE_URL", "REDIS_URL", "WORKER_API_TOKEN", "OIDC_CLIENT_SECRET"], env.name)])
    ])
    error_message = "Task definitions must inject secret ARN references and exclude sensitive values from plain environment."
  }
}
