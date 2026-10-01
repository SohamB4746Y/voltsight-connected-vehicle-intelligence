output "cluster_name" { value = google_container_cluster.main.name }
output "postgres_private_ip" { value = google_sql_database_instance.postgres.private_ip_address }
output "redis_host" { value = google_redis_instance.state.host }
output "cold_bucket" { value = google_storage_bucket.cold.name }
