# Gateway model entry for Foreman (shared infrastructure)

Foreman phases reach Bedrock through the LiteLLM gateway at
`bedrock-gateway.honcho.svc.cluster.local`, so agent pods hold no AWS credentials.
The gateway serves its own model names, and Foreman maps workflow model ids onto
them with `KELOS_MODEL_MAP`.

## Change made

The gateway did not serve any MiniMax model, but Foreman's bundled workflows use
`MiniMax` for the `documentation` phase. One entry was added to the
`bedrock-gateway-config` ConfigMap in the `honcho` namespace:

```yaml
  - model_name: "minimax"
    litellm_params:
      model: "bedrock/minimax.minimax-m2.5"
      aws_region_name: "us-east-1"
```

No IAM change was needed: the gateway's role (`eks-honcho-bedrock-gateway`, via EKS
Pod Identity) already allows `bedrock:InvokeModel` on `Resource: "*"`.

**This ConfigMap is shared.** It is not owned by Helm or Argo, and other services
in `honcho` use the same gateway. The entry is additive, but applying it requires
restarting the gateway, which briefly interrupts every consumer.

## Verified

- `minimax` appears in `/v1/models`; the other nine models are unaffected.
- Tool calling survives the Anthropic → MiniMax translation: a request with a
  `tools` array returned `stop_reason: tool_use` with intact arguments. Worth
  checking after any gateway upgrade, because `litellm_settings.drop_params: true`
  discards unsupported parameters silently rather than erroring.

## Notes

MiniMax is a reasoning model and emits a `thinking` block, so it consumes more
output tokens before answering than a comparable Claude model. Give phases using it
a higher `maxTurns`/token budget.

## Model map used by Foreman

```
KELOS_MODEL_MAP='anthropic/claude-haiku-4-5=claude-haiku,anthropic/claude-sonnet-4-6=claude-sonnet,anthropic/claude-opus-4-6=claude-opus,minimax/MiniMax-M2.7=minimax,minimax/MiniMax-M2.7-highspeed=minimax'
```

An unmapped model is refused rather than substituted, so a workflow that adds a new
model fails loudly instead of silently running on a different one.
