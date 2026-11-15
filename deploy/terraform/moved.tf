moved {
  from = aws_ecs_cluster.this
  to   = aws_ecs_cluster.this[0]
}

moved {
  from = aws_secretsmanager_secret.database
  to   = aws_secretsmanager_secret.database[0]
}

moved {
  from = aws_secretsmanager_secret.app
  to   = aws_secretsmanager_secret.app[0]
}

moved {
  from = aws_secretsmanager_secret.metrics_token
  to   = aws_secretsmanager_secret.metrics_token[0]
}
