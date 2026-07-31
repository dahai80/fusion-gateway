data "aws_iam_policy_document" "gateway_assume_role" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [module.eks.oidc_provider_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${module.eks.oidc_provider}:sub"
      values   = ["system:serviceaccount:${var.namespace}:fusion-gateway"]
    }
  }
}

resource "aws_iam_role" "gateway" {
  name               = "${var.cluster_name}-gateway-irsa"
  assume_role_policy = data.aws_iam_policy_document.gateway_assume_role.json
}

resource "aws_iam_role_policy_attachment" "gateway_readonly" {
  role       = aws_iam_role.gateway.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}
