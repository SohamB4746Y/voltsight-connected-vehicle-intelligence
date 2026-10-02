output "cluster_name" { value = aws_eks_cluster.main.name }
output "postgres_endpoint" { value = aws_db_instance.postgres.address }
output "redis_endpoint" { value = aws_elasticache_replication_group.redis.primary_endpoint_address }
output "kafka_bootstrap_tls" { value = aws_msk_cluster.kafka.bootstrap_brokers_tls }
output "cold_bucket" { value = aws_s3_bucket.cold.bucket }
output "helm_values_hint" {
  description = "values for deploy/helm/voltsight (config.*)"
  value = {
    kafkaBrokers = aws_msk_cluster.kafka.bootstrap_brokers_tls
    redisAddr    = "${aws_elasticache_replication_group.redis.primary_endpoint_address}:6379"
    postgresHost = aws_db_instance.postgres.address
  }
}
