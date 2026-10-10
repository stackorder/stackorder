locals {
  shared_outputs_direct = length(var.shared_outputs_state) > 0 && var.shared_outputs_role_arn == null
  shared_outputs_list_prefixes = {
    for s in var.shared_outputs_state : s.bucket => "${s.prefix}*"...
  }
  shared_outputs_kms_key_arns = distinct([for s in var.shared_outputs_state : s.kms_key_arn if s.kms_key_arn != null])

  ecs_tasks_assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = {
        StringEquals = { "aws:SourceAccount" = local.account_id }
        ArnLike      = { "aws:SourceArn" = "arn:${local.partition}:ecs:${local.region}:${local.account_id}:*" }
      }
    }]
  })
}

resource "aws_iam_role" "execution" {
  name               = "${var.name}-execution"
  description        = "Stackorder ECS task execution: pull the image, write logs, read the two secrets"
  assume_role_policy = local.ecs_tasks_assume_role_policy

  tags = var.tags
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "execution" {
  name = "secrets"
  role = aws_iam_role.execution.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat(
      [{
        Sid      = "ReadSecrets"
        Effect   = "Allow"
        Action   = ["secretsmanager:GetSecretValue"]
        Resource = [aws_secretsmanager_secret.database.arn, aws_secretsmanager_secret.app.arn]
      }],
      var.kms_key_arn == null ? [] : [{
        Sid      = "DecryptSecrets"
        Effect   = "Allow"
        Action   = ["kms:Decrypt"]
        Resource = [var.kms_key_arn]
        Condition = {
          StringEquals = { "kms:ViaService" = "secretsmanager.${local.region}.${local.dns_suffix}" }
        }
      }],
    )
  })
}

resource "aws_iam_role" "task" {
  name               = "${var.name}-task"
  description        = "Stackorder server: no AWS permissions unless the artifact bucket, shared outputs or ECS Exec is enabled"
  assume_role_policy = local.ecs_tasks_assume_role_policy

  tags = var.tags
}

resource "aws_iam_role_policy" "task_artifacts" {
  count = var.artifact_bucket_enabled ? 1 : 0

  name = "artifacts"
  role = aws_iam_role.task.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ListArtifacts"
        Effect   = "Allow"
        Action   = ["s3:ListBucket"]
        Resource = [aws_s3_bucket.artifacts[0].arn]
      },
      {
        Sid      = "ReadWriteArtifacts"
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
        Resource = ["${aws_s3_bucket.artifacts[0].arn}/*"]
      },
    ]
  })
}

resource "aws_iam_role_policy" "task_shared_outputs" {
  count = length(var.shared_outputs_state) > 0 ? 1 : 0

  name = "shared-outputs"
  role = aws_iam_role.task.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat(
      local.shared_outputs_direct ? [{
        Sid      = "ReadStateObjects"
        Effect   = "Allow"
        Action   = ["s3:GetObject"]
        Resource = distinct([for s in var.shared_outputs_state : "arn:${local.partition}:s3:::${s.bucket}/${s.prefix}*"])
      }] : [],
      local.shared_outputs_direct ? [
        for i, bucket in keys(local.shared_outputs_list_prefixes) : {
          Sid       = "ListStateBucket${i}"
          Effect    = "Allow"
          Action    = ["s3:ListBucket"]
          Resource  = ["arn:${local.partition}:s3:::${bucket}"]
          Condition = { StringLike = { "s3:prefix" = distinct(local.shared_outputs_list_prefixes[bucket]) } }
        }
      ] : [],
      local.shared_outputs_direct && length(local.shared_outputs_kms_key_arns) > 0 ? [{
        Sid      = "DecryptStateObjects"
        Effect   = "Allow"
        Action   = ["kms:Decrypt"]
        Resource = local.shared_outputs_kms_key_arns
        Condition = {
          StringEquals = { "kms:ViaService" = distinct([for arn in local.shared_outputs_kms_key_arns : "s3.${split(":", arn)[3]}.${local.dns_suffix}"]) }
        }
      }] : [],
      var.shared_outputs_role_arn == null ? [] : [{
        Sid      = "AssumeStateRole"
        Effect   = "Allow"
        Action   = ["sts:AssumeRole"]
        Resource = [var.shared_outputs_role_arn]
      }],
    )
  })
}

resource "aws_iam_role_policy" "task_execute_command" {
  count = var.enable_execute_command ? 1 : 0

  name = "execute-command"
  role = aws_iam_role.task.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid    = "EcsExec"
      Effect = "Allow"
      Action = [
        "ssmmessages:CreateControlChannel",
        "ssmmessages:CreateDataChannel",
        "ssmmessages:OpenControlChannel",
        "ssmmessages:OpenDataChannel",
      ]
      Resource = ["*"]
    }]
  })
}
