#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
compose() { docker compose -f docker-compose.yml -f docker-compose.app.yml -f docker-compose.ecs.yml "$@"; }
case "${1:-}" in
  up)
    docker info >/dev/null
    compose build api worker web
    compose up -d --wait postgres redis localstack
    compose exec -T localstack awslocal s3api put-bucket-cors --bucket watermarker --cors-configuration file:///etc/localstack/init/ready.d/cors.json
    compose run --rm --no-deps migrate
    # Only public local demo values; real AWS values are supplied separately.
    compose exec -T localstack python3 - <<'PYSECRETS'
import boto3
client = boto3.client("secretsmanager", region_name="us-east-1", endpoint_url="http://localhost:4566", aws_access_key_id="test", aws_secret_access_key="test")
values = {
    "database_url": "postgres://watermarker:watermarker@postgres:5432/watermarker?sslmode=disable",
    "redis_url": "redis://redis:6379/0",
    "worker_api_token": "local-ecs-worker-token",
}
for name, value in values.items():
    name = "watermarker-ecs-local/" + name
    try:
        client.create_secret(Name=name, SecretString=value)
    except client.exceptions.ResourceExistsException:
        client.put_secret_value(SecretId=name, SecretString=value)
PYSECRETS
    tag="ecs-$(date +%s)-$$"
    for image in api worker web; do
      docker tag "watermarker-$image:local" "watermarker-$image:$tag"
    done
    # CreateCluster is idempotent. Recover the emulator's cluster before service refresh
    # when PERSISTENCE=0 erased it; the AWS provider retries ClusterNotFound otherwise.
    compose exec -T localstack awslocal ecs create-cluster --cluster-name watermarker-ecs-local --query cluster.clusterArn --output text >/dev/null
    terraform -chdir=infra/ecs-local init -backend=false -lockfile=readonly
    AWS_SESSION_TOKEN= AWS_PROFILE= terraform -chdir=infra/ecs-local apply -var="image_tag=$tag"
    curl --fail --silent --show-error --retry 30 --retry-delay 2 --retry-connrefused --max-time 5 http://localhost:5180/api/ping
    printf '\nWeb: http://localhost:5180\n'
    ;;
  down)
    AWS_SESSION_TOKEN= AWS_PROFILE= terraform -chdir=infra/ecs-local destroy -var=image_tag=unused
    ;;
  *) echo 'Usage: scripts/ecs-local.sh up|down' >&2; exit 2 ;;
esac
